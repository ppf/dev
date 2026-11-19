package serve

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/big"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const (
	// CodeTTL is how long a pairing code from dev serve pair stays valid.
	CodeTTL     = 5 * time.Minute
	maxMisses   = 5
	cookieName  = "dev_session"
	cookieAge   = 400 * 24 * time.Hour // browsers' cap
	maxDevice   = 40
	codeFile    = "pair.json"
	sessionFile = "sessions.json"
)

// Auth is the phone page's pairing and sessions, kept in Dir, which dev serve pair and unpair write too. The Mac
// keeps only the SHA-256 of a session token; a code's hash guards it only by its 5 minutes and single use.
type Auth struct {
	Dir string
	Now func() time.Time
}

type pending struct {
	Hash    string `json:"code_sha256"`
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
	unlock, err := lock(dir)
	if err != nil {
		return "", err
	}
	defer unlock()
	if err := writeFile(dir, codeFile, pending{Hash: hash(code), Expires: now.Add(CodeTTL).Unix()}); err != nil {
		return "", err
	}
	return code[:4] + " " + code[4:], nil
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

func (a *Auth) paired(r *http.Request) (bool, error) {
	c, err := r.Cookie(cookieName)
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
	if a.Now().Unix() >= p.Expires {
		a.drop(w, http.StatusUnauthorized, "the code has expired: run dev serve pair on the Mac again")
		return
	}
	if subtle.ConstantTimeCompare([]byte(hash(code)), []byte(p.Hash)) != 1 {
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
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", MaxAge: int(cookieAge.Seconds()),
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
