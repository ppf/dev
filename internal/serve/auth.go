package serve

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const (
	// CodeTTL is how long a pairing code from dev serve pair stays valid.
	CodeTTL      = 5 * time.Minute
	maxMisses    = 5
	cookiePrefix = "dev_session"
	cookieAge    = 400 * 24 * time.Hour // browsers' cap
	maxDevice    = 40
	codeFile     = "pair.json"
	addrFile     = "addr.json"
	sessionFile  = "sessions.json"
)

// Auth is the phone page's pairing and sessions, kept in Dir, which dev serve pair and unpair write too. The Mac
// keeps only the SHA-256 of a session token and a slow salted hash of a code.
type Auth struct {
	Dir string
	Now func() time.Time
}

// defaultCodeIter is PBKDF2-SHA256's iteration count for a code: about 100 ms a guess on an Apple M-series core, so
// trying all 10^8 codes takes months there (hours on a GPU farm): far past the code's 5 minutes, which is all it needs.
const defaultCodeIter = 1_000_000

var codeIter = defaultCodeIter // tests lower it

type pending struct {
	Hash    string `json:"code_pbkdf2_sha256"`
	Salt    string `json:"salt"`
	Iter    int    `json:"iterations"`
	Expires int64  `json:"expires"`
	Misses  int    `json:"misses,omitempty"`
}

type session struct {
	Device string `json:"device"`
	Hash   string `json:"token_sha256"`
	Paired int64  `json:"paired"`
}

// NewCode writes a new one-time pairing code to dir, replacing a pending one, and returns it as "1234 5678".
func NewCode(dir string, now time.Time) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("%08d", n.Int64())
	salt := make([]byte, 16)
	_, _ = rand.Read(salt) // never fails: it crashes instead
	unlock, err := lock(dir)
	if err != nil {
		return "", err
	}
	defer unlock()
	p := pending{Salt: hex.EncodeToString(salt), Iter: codeIter, Expires: now.Add(CodeTTL).Unix()}
	if p.Hash, err = p.derive(code); err != nil {
		return "", err
	}
	if err := writeFile(dir, codeFile, p); err != nil {
		return "", err
	}
	return code[:4] + " " + code[4:], nil
}

// RecordAddr writes the address the running dev serve listens on to dir, for dev serve pair; the func removes it
// unless another dev serve has recorded its own since, and puts back the one it replaced while that one still
// answers (a hand-run dev serve for the phone beside dev boss's own on loopback).
func RecordAddr(dir string, ap netip.AddrPort) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	prev, _, perr := Served(dir, time.Second)
	if err := writeFile(dir, addrFile, ap.String()); err != nil {
		return nil, err
	}
	return func() {
		var cur string
		if readFile(dir, addrFile, &cur) != nil || cur != ap.String() {
			return
		}
		if perr == nil && prev != ap && writeFile(dir, addrFile, prev.String()) == nil {
			if _, _, err := Served(dir, time.Second); err == nil {
				return
			}
		}
		_ = os.Remove(filepath.Join(dir, addrFile))
	}, nil
}

// ErrNotRunning is Served's error when no dev serve answers: no record, or nothing answers /v1/health there.
var ErrNotRunning = errors.New("dev serve is not running")

// ServedAddr is the address the running dev serve recorded in dir; ErrNotRunning when none runs.
func ServedAddr(dir string) (netip.AddrPort, error) {
	ap, _, err := Served(dir, time.Second)
	return ap, err
}

// Served is ServedAddr with the version its /v1/health names, waiting at most timeout for it.
func Served(dir string, timeout time.Duration) (netip.AddrPort, string, error) {
	var s string
	if err := readFile(dir, addrFile, &s); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return netip.AddrPort{}, "", fmt.Errorf("%w: start it first (a dev serve started before this version must be restarted)", ErrNotRunning)
		}
		_, syntax := errors.AsType[*json.SyntaxError](err)
		_, typ := errors.AsType[*json.UnmarshalTypeError](err)
		if syntax || typ { // a record no dev serve wrote whole
			return netip.AddrPort{}, "", fmt.Errorf("%w: %v", ErrNotRunning, err)
		}
		return netip.AddrPort{}, "", err
	}
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return ap, "", fmt.Errorf("%w: %s: %v", ErrNotRunning, filepath.Join(dir, addrFile), err)
	}
	// A Transport without Proxy: HTTP_PROXY must not take a check of this Mac's own address
	notRunning := fmt.Errorf("%w: it does not answer on %s", ErrNotRunning, s)
	resp, err := (&http.Client{Timeout: timeout, Transport: &http.Transport{DisableKeepAlives: true},
		// Only a direct answer counts: another program on a stale record's port could redirect to one
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get("http://" + s + "/v1/health")
	if err != nil {
		return ap, "", notRunning
	}
	defer func() { _ = resp.Body.Close() }()
	var health struct {
		OK      bool
		Version string
	}
	// Another program on a stale record's port answers no {"ok":true,"version":…}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health) != nil || !health.OK || health.Version == "" {
		return ap, "", notRunning
	}
	return ap, health.Version, nil
}

// Unpair drops the session of device, or every session when device is "", and returns the devices dropped.
func Unpair(dir, device string) ([]string, error) {
	if device != "" {
		if device = deviceName(device); device == "" {
			return nil, errors.New("the device name is blank")
		}
	}
	unlock, err := lock(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	sessions, err := readSessions(dir)
	if err != nil {
		return nil, err
	}
	var kept []session
	var dropped []string
	for _, s := range sessions {
		if device == "" || s.Device == device {
			dropped = append(dropped, s.Device)
		} else {
			kept = append(kept, s)
		}
	}
	if len(dropped) == 0 {
		return nil, nil
	}
	return dropped, writeFile(dir, sessionFile, kept)
}

// Session is the Guard: the request carries a paired device's session cookie, else 401 (500 if sessions can't be read).
// It reads sessions.json without the lock: writeFile replaces it by a rename.
func (a *Auth) Session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, err := a.paired(r)
		if err != nil {
			log.Printf("dev serve: sessions not read: %v", err)
			writeError(w, http.StatusInternalServerError, "the sessions couldn't be read: see dev serve's output")
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "this phone isn't paired: run dev serve pair on the Mac and enter its code")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cookieName is per port: browsers scope cookies by host only, so two dev serves on one host would share one.
func cookieName(r *http.Request) string {
	if _, port, err := net.SplitHostPort(r.Host); err == nil && isDigits(port) {
		return cookiePrefix + "_" + port
	}
	return cookiePrefix
}

func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

func (a *Auth) paired(r *http.Request) (bool, error) {
	c, err := r.Cookie(cookieName(r))
	if err != nil {
		return false, nil
	}
	sessions, err := readSessions(a.Dir)
	if err != nil {
		return false, err
	}
	sum := []byte(hash(c.Value))
	found := 0
	for _, s := range sessions {
		found |= subtle.ConstantTimeCompare(sum, []byte(s.Hash))
	}
	return found == 1, nil
}

// Pair is POST /v1/pair {"code","device"}: a pending, unexpired code sets a new session cookie and is used up.
// maxMisses wrong codes void it. It needs JSON or X-Dev-Page: 1, which a cross-site page can't send without a CORS
// preflight this server never answers, so another site can't burn the code's tries.
func (a *Auth) Pair(w http.ResponseWriter, r *http.Request) {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" && r.Header.Get("X-Dev-Page") != "1" {
		writeError(w, http.StatusBadRequest, "send the pairing as Content-Type: application/json")
		return
	}
	var req struct{ Code, Device string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, `send {"code":"…","device":"…"}`)
		return
	}
	device := deviceName(req.Device)
	if device == "" {
		writeError(w, http.StatusBadRequest, "name the device")
		return
	}
	code := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, req.Code)

	unlock, err := lock(a.Dir)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer unlock()
	var p pending
	switch err := readFile(a.Dir, codeFile, &p); {
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusUnauthorized, "no pairing code is pending: run dev serve pair on the Mac")
		return
	case err != nil:
		a.fail(w, err)
		return
	}
	if !p.checkable() {
		a.drop(w, http.StatusUnauthorized, "this dev serve can't check the pending code (restart dev serve after updating dev): run dev serve pair on the Mac again")
		return
	}
	if a.Now().Unix() >= p.Expires {
		a.drop(w, http.StatusUnauthorized, "the code has expired: run dev serve pair on the Mac again")
		return
	}
	got, err := p.derive(code)
	if err != nil {
		a.fail(w, err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(p.Hash)) != 1 {
		if p.Misses++; p.Misses >= maxMisses {
			log.Printf("dev serve: pairing code voided after %d wrong codes", maxMisses)
			a.drop(w, http.StatusTooManyRequests, "too many wrong codes: run dev serve pair on the Mac again")
			return
		}
		if err := writeFile(a.Dir, codeFile, p); err != nil {
			a.fail(w, err)
			return
		}
		writeError(w, http.StatusUnauthorized, "wrong code")
		return
	}
	// Used up before the session is stored: a failure after this needs a new code, never a second use
	if err := os.Remove(filepath.Join(a.Dir, codeFile)); err != nil {
		a.fail(w, err)
		return
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw) // never fails: it crashes instead
	token := hex.EncodeToString(raw)
	sessions, err := readSessions(a.Dir)
	if err != nil {
		a.fail(w, err)
		return
	}
	kept := []session{{Device: device, Hash: hash(token), Paired: a.Now().Unix()}}
	for _, s := range sessions {
		if s.Device != device {
			kept = append(kept, s)
		}
	}
	if err := writeFile(a.Dir, sessionFile, kept); err != nil {
		a.fail(w, err)
		return
	}
	log.Printf("dev serve: a phone paired")
	http.SetCookie(w, &http.Cookie{Name: cookieName(r), Value: token, Path: "/", MaxAge: int(cookieAge.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]string{"device": device})
}

// drop voids the pending code and answers code with msg.
func (a *Auth) drop(w http.ResponseWriter, code int, msg string) {
	if err := os.Remove(filepath.Join(a.Dir, codeFile)); err != nil {
		a.fail(w, err)
		return
	}
	writeError(w, code, msg)
}

func (a *Auth) fail(w http.ResponseWriter, err error) {
	log.Printf("dev serve: pairing failed: %v", err)
	writeError(w, http.StatusInternalServerError, "pairing failed: see dev serve's output, then run dev serve pair again")
}

// deviceName is name without control or format characters (unpair prints it), trimmed, at most maxDevice runes.
func deviceName(name string) string {
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name))
	if r := []rune(name); len(r) > maxDevice {
		name = strings.TrimSpace(string(r[:maxDevice]))
	}
	return name
}

// checkable is whether p is a code record this build wrote: its hash, a 16-byte salt and a sane iteration count, so
// a stale or damaged file can't stall pairing or pass as a wrong code.
func (p pending) checkable() bool {
	salt, err := hex.DecodeString(p.Salt)
	return p.Hash != "" && err == nil && len(salt) == 16 && p.Iter >= 1 && p.Iter <= 10*defaultCodeIter
}

// derive is code's PBKDF2-SHA256 under p's salt and iterations.
func (p pending) derive(code string) (string, error) {
	salt, err := hex.DecodeString(p.Salt)
	if err != nil {
		return "", fmt.Errorf("pairing code salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, code, salt, p.Iter, 32)
	if err != nil {
		return "", fmt.Errorf("pairing code hash: %w", err)
	}
	return hex.EncodeToString(key), nil
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// lock takes dir's lock, shared by dev serve and dev serve pair and unpair; the func releases it.
func lock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", dir, err)
	}
	return func() { _ = f.Close() }, nil
}

// Alive marks a running dev serve on dir until the file is closed (or the process exits): a shared lock on dir/alive.
func Alive(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, "alive"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	return f, nil
}

// AnyAlive is whether a dev serve holds dir's Alive lock, answering or not.
func AnyAlive(dir string) (bool, error) {
	f, err := os.OpenFile(filepath.Join(dir, "alive"), os.O_RDWR, 0) // an exclusive flock wants a writable one on NFS
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
	case errors.Is(err, syscall.EWOULDBLOCK):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	return false, nil
}

func readSessions(dir string) ([]session, error) {
	var s []session
	if err := readFile(dir, sessionFile, &s); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func readFile(dir, name string, v any) error {
	path := filepath.Join(dir, name)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// writeFile replaces dir/name with v as JSON, mode 0600, by a rename.
func writeFile(dir, name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err := errors.Join(err, f.Close()); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}
