// Package telegram wraps gotd: connection lifecycle, session reuse and web-driven login.
package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"golang.org/x/time/rate"
	"rsc.io/qr"
)

type Mode string

const (
	ModeQR    Mode = "qr"
	ModePhone Mode = "phone"
)

type State string

const (
	StateNeedCreds    State = "need_creds"
	StateConnecting   State = "connecting"
	StateQR           State = "qr"
	StateNeedPhone    State = "need_phone"
	StateNeedCode     State = "need_code"
	StateNeedPassword State = "need_password"
	StateAuthorized   State = "authorized"
)

// Status is a snapshot for the UI.
type Status struct {
	State State  `json:"state"`
	User  string `json:"user,omitempty"`
	Error string `json:"error,omitempty"`
}

type Client struct {
	mode        Mode
	sessionPath string
	rateLimit   float64

	mu    sync.Mutex
	creds struct {
		id   int
		hash string
	}
	st  Status
	qr  []byte // PNG of the current login token
	api *tg.Client

	cancelRun  context.CancelFunc // stops the current connection (used by Logout)
	loggingOut bool

	credsCh chan struct{} // signals new credentials
	inCh    chan string   // phone / code / password typed in the web form
}

func New(mode Mode, sessionPath string, apiID int, apiHash string, rateLimit float64) *Client {
	c := &Client{
		mode: mode, sessionPath: sessionPath, rateLimit: rateLimit,
		credsCh: make(chan struct{}, 1), inCh: make(chan string, 1),
	}
	c.creds.id, c.creds.hash = apiID, apiHash
	return c
}

func (c *Client) Status() Status { c.mu.Lock(); defer c.mu.Unlock(); return c.st }

// QRPNG returns the current login QR, if one is being shown.
func (c *Client) QRPNG() ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.qr, c.qr != nil
}

// API returns the MTProto client once authorized.
func (c *Client) API() (*tg.Client, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.api, c.api != nil
}

func (c *Client) set(s State, errMsg string) {
	c.mu.Lock()
	c.st.State, c.st.Error = s, errMsg
	if s != StateQR {
		c.qr = nil
	}
	c.mu.Unlock()
}

// Logout ends the session on Telegram's side, deletes the local session file and returns the
// client to the login screen, so another account can sign in.
func (c *Client) Logout(ctx context.Context) error {
	api, ok := c.API()
	if !ok {
		return errors.New("not signed in")
	}
	// An already-dead session is as good as logged out.
	if _, err := api.AuthLogOut(ctx); err != nil && !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
		return fmt.Errorf("log out: %w", err)
	}
	c.mu.Lock()
	c.loggingOut = true
	cancel := c.cancelRun
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// SetCreds supplies api_id/api_hash when they were not in config/env.
func (c *Client) SetCreds(id int, hash string) {
	c.mu.Lock()
	c.creds.id, c.creds.hash = id, hash
	c.mu.Unlock()
	select {
	case c.credsCh <- struct{}{}:
	default:
	}
}

// Submit delivers a form value; it is accepted only in the matching need_* state.
func (c *Client) Submit(want State, v string) error {
	if c.Status().State != want {
		return fmt.Errorf("not waiting for %s", want)
	}
	select {
	case c.inCh <- strings.TrimSpace(v):
		return nil
	default:
		return errors.New("input already pending")
	}
}

// Run keeps the client connected until ctx is cancelled, retrying after failures.
func (c *Client) Run(ctx context.Context) {
	for ctx.Err() == nil {
		c.mu.Lock()
		id, hash := c.creds.id, c.creds.hash
		c.mu.Unlock()
		if id == 0 || hash == "" {
			c.set(StateNeedCreds, "")
			select {
			case <-c.credsCh:
			case <-ctx.Done():
			}
			continue
		}
		c.set(StateConnecting, "")
		rctx, cancel := context.WithCancel(ctx)
		c.mu.Lock()
		c.cancelRun = cancel
		c.mu.Unlock()
		err := c.runOnce(rctx, id, hash)
		cancel()
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		c.api, c.cancelRun = nil, nil
		out := c.loggingOut
		c.loggingOut = false
		c.mu.Unlock()
		if out { // deliberate logout: drop the saved session and go straight to a fresh login
			if err := os.Remove(c.sessionPath); err != nil && !os.IsNotExist(err) {
				c.set(StateConnecting, "logged out, but could not delete the session file: "+err.Error())
			}
			continue
		}
		c.set(StateConnecting, fmt.Sprint(err))
		select { // retry later, or right away if creds changed
		case <-time.After(5 * time.Second):
		case <-c.credsCh:
		case <-ctx.Done():
		}
	}
}

func (c *Client) runOnce(ctx context.Context, id int, hash string) error {
	if err := os.MkdirAll(filepath.Dir(c.sessionPath), 0o700); err != nil {
		return err
	}
	d := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(d) // must be registered before the client starts
	waiter := floodwait.NewWaiter()
	client := telegram.NewClient(id, hash, telegram.Options{
		SessionStorage: &telegram.FileSessionStorage{Path: c.sessionPath},
		UpdateHandler:  d,
		Middlewares:    []telegram.Middleware{waiter, exemptFileChunks{ratelimit.New(rate.Limit(c.rateLimit), 5)}},
	})
	return waiter.Run(ctx, func(ctx context.Context) error {
		return client.Run(ctx, func(ctx context.Context) error {
			// A valid saved session is reused: no login at all.
			st, err := client.Auth().Status(ctx)
			if err != nil {
				return err
			}
			if !st.Authorized {
				if err := c.login(ctx, client, loggedIn); err != nil {
					return err
				}
			}
			self, err := client.Self(ctx)
			if err != nil {
				return err
			}
			name := strings.TrimSpace(self.FirstName + " " + self.LastName)
			if self.Username != "" {
				name += " (@" + self.Username + ")"
			}
			c.mu.Lock()
			c.api, c.st = client.API(), Status{State: StateAuthorized, User: name}
			c.qr = nil
			c.mu.Unlock()
			<-ctx.Done()
			return ctx.Err()
		})
	})
}

func (c *Client) login(ctx context.Context, client *telegram.Client, loggedIn qrlogin.LoggedIn) error {
	if c.mode == ModePhone {
		return client.Auth().IfNecessary(ctx, auth.NewFlow(webAuth{c}, auth.SendCodeOptions{}))
	}
	_, err := client.QR().Auth(ctx, loggedIn, func(_ context.Context, t qrlogin.Token) error {
		png, err := renderQR(t.URL())
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.qr, c.st = png, Status{State: StateQR}
		c.mu.Unlock()
		return nil
	})
	if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") { // account has 2FA
		return c.password(ctx, client)
	}
	return err
}

// renderQR draws the code module by module (8px each) with a 4-module white quiet zone.
// We avoid qrlogin.Token.Image: its bounds and pixels disagree and it yields a tiny corner QR.
func renderQR(url string) ([]byte, error) {
	const scale, quiet = 8, 4
	code, err := qr.Encode(url, qr.M)
	if err != nil {
		return nil, err
	}
	n := (code.Size + 2*quiet) * scale
	img := image.NewGray(image.Rect(0, 0, n, n))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if !code.Black(x, y) {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.SetGray((x+quiet)*scale+dx, (y+quiet)*scale+dy, color.Gray{})
				}
			}
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// password asks for the 2FA password until it is accepted.
func (c *Client) password(ctx context.Context, client *telegram.Client) error {
	msg := ""
	for {
		pw, err := c.ask(ctx, StateNeedPassword, msg)
		if err != nil {
			return err
		}
		_, err = client.Auth().Password(ctx, pw)
		if !errors.Is(err, auth.ErrPasswordInvalid) {
			return err
		}
		msg = "Wrong password"
	}
}

func (c *Client) ask(ctx context.Context, s State, errMsg string) (string, error) {
	select { // drop a stale value
	case <-c.inCh:
	default:
	}
	c.set(s, errMsg)
	select {
	case v := <-c.inCh:
		return v, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// exemptFileChunks keeps bulk upload.getFile traffic out of the API rate limiter: with 1 MB chunks
// a 5 req/s limit would cap all downloads at ~5 MB/s. FLOOD_WAIT is still handled by floodwait.
type exemptFileChunks struct{ limiter telegram.Middleware }

func (e exemptFileChunks) Handle(next tg.Invoker) telegram.InvokeFunc {
	limited := e.limiter.Handle(next)
	return func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.UploadGetFileRequest); ok {
			return next.Invoke(ctx, in, out)
		}
		return limited(ctx, in, out)
	}
}

// webAuth feeds the phone/code/password flow from the web form.
type webAuth struct{ c *Client }

func (w webAuth) Phone(ctx context.Context) (string, error) { return w.c.ask(ctx, StateNeedPhone, "") }
func (w webAuth) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	return w.c.ask(ctx, StateNeedCode, "")
}
func (w webAuth) Password(ctx context.Context) (string, error) {
	return w.c.ask(ctx, StateNeedPassword, "")
}
func (webAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error { return nil }
func (webAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("this phone number has no Telegram account")
}
