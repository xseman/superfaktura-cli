package tui_test

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// What the browser actually draws.
//
// The model tests feed it messages and read its state back, which is the right
// shape for the quota rules and useless for everything a person complains
// about: a select that offered no options, a key the pager swallowed before the
// action saw it, labels that were whole sentences, a theme style that dropped
// the string it was given. Every one of those was found by driving a pty by
// hand and looking at the screen, and none of them left a test behind.
//
// This is that session, written down. The binary runs against a local server
// serving canned responses, its output is replayed onto a grid (see vt_test.go)
// and each frame is compared with a committed golden file. Regenerate them with
//
//	go test ./internal/tui -run TestTheBrowserDrawsItsFrames -update
//
// and read the diff before committing it: the golden is the layout, so a change
// to it is a change to what people see.

var update = flag.Bool("update", false, "rewrite the golden frames in testdata")

// errUnsupportedPTY marks the one platform-specific part of this test, so a
// system without a pty reads as "not run" rather than as a failure.
var errUnsupportedPTY = errors.New("no pty on this platform")

const (
	screenWidth  = 100
	screenHeight = 32
)

func TestTheBrowserDrawsItsFrames(t *testing.T) {
	binary := build(t)
	server := fixtureServer(t)

	term := newTerminal(screenWidth, screenHeight)
	session := start(t, binary, server.URL, term)

	// One session, walked the way a person walks it. Each step names the keys
	// it sends and a marker that says the frame it asked for has arrived —
	// without one the capture races the redraw and the golden is whatever the
	// screen happened to be halfway through.
	steps := []struct {
		name  string
		keys  []string
		until string
	}{
		{"overview", nil, "Overdue"},
		{"invoices", []string{"\t"}, "2026001"},
		{"invoice-expanded", []string{"\r"}, "esc back"},
		{"invoice-delete-confirm", []string{esc, "d"}, "Delete invoice"},
		{"invoice-create-form", []string{esc, "n"}, "Existing client"},
		{"invoices-unpaid", []string{esc, "f"}, "Unpaid"},
	}

	for _, step := range steps {
		for _, key := range step.keys {
			session.send(t, key)
		}
		session.settle(t, step.name, step.until)
		compare(t, step.name, session.frame())
	}
}

const esc = "\x1b"

// build compiles the binary under test. The browser is the whole program —
// resources, columns and actions come from internal/commands — so there is
// nothing smaller to drive.
func build(t *testing.T) string {
	t.Helper()
	toolchain, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on PATH to build the binary with")
	}

	binary := filepath.Join(t.TempDir(), "sf")
	cmd := exec.Command(toolchain, "build", "-o", binary, "./cmd/sf")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the binary: %v\n%s", err, out)
	}
	return binary
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// session is the running browser and the screen it is drawing on.
type session struct {
	master *os.File
	term   *terminal
	mu     *sync.Mutex
}

func start(t *testing.T, binary, apiURL string, term *terminal) *session {
	t.Helper()

	master, slaveName, err := openPTY(screenWidth, screenHeight)
	if err != nil {
		if errors.Is(err, errUnsupportedPTY) {
			t.Skip("the frame test needs a pseudo-terminal")
		}
		t.Fatalf("opening a pty: %v", err)
	}
	slave, err := os.OpenFile(slaveName, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", slaveName, err)
	}

	home := t.TempDir()
	cmd := exec.Command(binary, "ui")
	cmd.Dir = t.TempDir()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = sessionAttrs()
	// A fresh environment, not the developer's: TERM and COLORTERM decide how
	// lipgloss renders every color in the frame, and the golden records them.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"TERM=xterm-256color",
		"LC_ALL=C.UTF-8",
		"SF_API_URL=" + apiURL,
		"SF_EMAIL=me@example.com",
		"SF_APIKEY=k",
		"SF_COMPANY_ID=1",
		"SF_CONFIG_DIR=" + filepath.Join(home, "config"),
		"SF_NO_KEYRING=1",
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the browser: %v", err)
	}
	_ = slave.Close()

	s := &session{master: master, term: term, mu: &sync.Mutex{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				s.mu.Lock()
				_, _ = term.Write(buf[:n])
				s.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = master.Close()
		<-done
	})
	return s
}

func (s *session) send(t *testing.T, keys string) {
	t.Helper()
	if _, err := s.master.WriteString(keys); err != nil {
		t.Fatalf("sending %q: %v", keys, err)
	}
	// Escape is a key on its own here and also the first byte of every arrow
	// key. Sent back to back with what follows it, the child reads one
	// sequence instead of two keystrokes.
	time.Sleep(60 * time.Millisecond)
}

// settle waits for the frame the step asked for and then for the screen to
// stop changing, so nothing is captured mid-redraw.
func (s *session) settle(t *testing.T, name, marker string) {
	t.Helper()
	const (
		timeout = 15 * time.Second
		quiet   = 250 * time.Millisecond
	)

	deadline := time.Now().Add(timeout)
	last, stableSince := "", time.Time{}
	for time.Now().Before(deadline) {
		screen := s.plain()
		switch {
		case !strings.Contains(screen, marker):
			stableSince = time.Time{}
		case screen != last:
			stableSince = time.Now()
		case time.Since(stableSince) >= quiet:
			return
		}
		last = screen
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s: %q never appeared on a settled screen:\n%s", name, marker, s.plain())
}

func (s *session) plain() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.term.text(), "\n")
}

// frame is the screen as the golden file records it.
func (s *session) frame() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.term.normaliseAges()
	return s.term.dump()
}

// compare checks a frame against its golden, or rewrites it under -update.
func compare(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")

	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatalf("testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v — run 'go test ./internal/tui -run TestTheBrowserDrawsItsFrames -update' to create it", err)
	}
	if string(want) == got {
		return
	}
	t.Errorf("%s no longer draws what %s records:\n%s", name, path, difference(string(want), got))
}

// difference reports the lines that changed, which on a 32-line frame with a
// style listing under it is far easier to read than both frames in full.
func difference(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	shown := 0
	for i := range max(len(wantLines), len(gotLines)) {
		w, g := at(wantLines, i), at(gotLines, i)
		if w == g {
			continue
		}
		if shown == 12 {
			fmt.Fprintf(&b, "  … and more\n")
			break
		}
		fmt.Fprintf(&b, "  - %s\n  + %s\n", w, g)
		shown++
	}
	return b.String()
}

func at(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "<end of frame>"
}

// dump is the golden format: the screen with its lines numbered and bracketed,
// so trailing blanks and the exact number of lines are both visible, and then
// the appearance of every styled run.
func (t *terminal) dump() string {
	var b strings.Builder
	fmt.Fprintf(&b, "screen %dx%d\n", t.width, t.height)
	for i, line := range t.text() {
		fmt.Fprintf(&b, "%2d |%s|\n", i, line)
	}
	b.WriteString("\nstyles\n")
	for _, run := range t.styleRuns() {
		fmt.Fprintf(&b, "%2d %3d-%-3d %s\n", run.row, run.from, run.to, run.style)
	}
	return b.String()
}

// ages are the one thing on screen that moves on its own.
var ageText = regexp.MustCompile(`(just now|\d+[smh] ago)`)

// normaliseAges rewrites "fetched 2s ago" to "fetched just now".
//
// The frame is drawn when something happens and not again, so the age is
// usually the one it was written with — but a slow machine can put a second
// between the load and the keystroke that redraws, and a golden file that
// depends on that is flaky by construction. The label is left alone: "cached"
// against "fetched" is the fact worth pinning, and only the number moves.
func (t *terminal) normaliseAges() {
	const canonical = "just now"
	for y, row := range t.cells {
		runes := make([]rune, len(row))
		for x, c := range row {
			runes[x] = c.r
		}
		line := string(runes)
		match := ageText.FindStringIndex(line)
		if match == nil {
			continue
		}
		// Only when the age ends the line, which is where the filter band puts
		// it. Anywhere else, rewriting it would shift what follows.
		if strings.TrimSpace(line[match[1]:]) != "" {
			continue
		}
		from := utf8.RuneCountInString(line[:match[0]])
		appearance := row[from].style
		for x := from; x < t.width; x++ {
			at := x - from
			if at < len(canonical) {
				t.cells[y][x] = cell{r: rune(canonical[at]), style: appearance}
				continue
			}
			t.cells[y][x] = cell{r: ' '}
		}
	}
}

// fixtureServer answers with SuperFaktura-shaped records, fixed so the frame
// is the same every run.
//
// The dates are deliberately in the past: an invoice's badge and the overview's
// buckets are computed against today, so a fixture due next week would change
// what the golden says the moment the week turned.
func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "application/json")
		// The quota is in the header of every frame, and it comes from these.
		h.Set("X-RateLimit-DailyLimit", "1000")
		h.Set("X-RateLimit-DailyRemaining", "876")
		h.Set("X-RateLimit-DailyReset", "02.08.2026 00:00:00")
		h.Set("X-RateLimit-MonthlyLimit", "31000")
		h.Set("X-RateLimit-MonthlyRemaining", "13995")

		path := r.URL.Path
		unpaidOnly := strings.Contains(path, "status:1|2")
		switch {
		case strings.HasPrefix(path, "/invoices/index.json"):
			_, _ = w.Write([]byte(invoiceList(unpaidOnly)))
		case strings.HasPrefix(path, "/expenses/index.json"):
			_, _ = w.Write([]byte(expenseList(unpaidOnly)))
		case strings.HasPrefix(path, "/clients/index.json"):
			_, _ = w.Write([]byte(clientList))
		case strings.HasPrefix(path, "/tags/index.json"):
			_, _ = w.Write([]byte(`{"11":"sf-cli test","12":"urgent"}`))
		default:
			_, _ = w.Write([]byte(`{"error":0,"error_message":"ok"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The rows are trimmed copies of live responses: a list row is a complete
// record, which is why the detail pane below the list costs no request.
const (
	unpaidInvoice = `{
	  "Invoice":{"id":"309127","invoice_no_formatted":"2026001","type":"regular",
	    "created":"2024-03-01","due":"2024-03-15","delivery":"2024-03-01","variable":"2026001",
	    "amount":"640.00","vat":"147.20","total_amount":"787.20","amount_paid":"300.00",
	    "invoice_currency":"EUR","token":"tok1"},
	  "Client":{"name":"Acme s.r.o."},
	  "0":{"total":"787.200000","to_pay":"487.200000"},
	  "InvoiceItem":[
	    {"id":"804902","name":"Konzultácie","quantity":"8.00000","unit_price":75,"tax":23,"item_price_vat":738},
	    {"id":"804904","name":"Cestovné","quantity":"1.00000","unit_price":40,"tax":23,"item_price_vat":49.2}],
	  "InvoicePayment":[{"created":"2024-03-05 00:00:00","payment_type":"transfer","amount":"300.00"}]}`

	partialInvoice = `{
	  "Invoice":{"id":"309129","invoice_no_formatted":"2026003","type":"proforma",
	    "created":"2024-04-02","due":"2024-04-16","variable":"2026003",
	    "amount":"4.50","vat":"0.00","total_amount":"4.50","amount_paid":"0.00",
	    "invoice_currency":"EUR","token":"tok3"},
	  "Client":{"name":"Gamma s.r.o."},
	  "0":{"total":"4.500000","to_pay":"4.500000"},
	  "InvoiceItem":[{"id":"804910","name":"Doména","quantity":"1.00000","unit_price":4.5,"tax":0,"item_price_vat":4.5}],
	  "InvoicePayment":[]}`

	paidInvoice = `{
	  "Invoice":{"id":"309128","invoice_no_formatted":"2026002","type":"regular",
	    "created":"2024-03-20","due":"2024-04-03","paydate":"2024-03-25","variable":"2026002",
	    "amount":"99.00","vat":"0.00","total_amount":"99.00","amount_paid":"99.00",
	    "invoice_currency":"EUR","token":"tok2"},
	  "Client":{"name":"Beta a.s."},
	  "0":{"total":"99.000000","to_pay":"0.000000"},
	  "InvoiceItem":[{"id":"804911","name":"Hosting","quantity":"1.00000","unit_price":99,"tax":0,"item_price_vat":99}],
	  "InvoicePayment":[{"created":"2024-03-25 00:00:00","payment_type":"card","amount":"99.00"}]}`

	unpaidExpense = `{
	  "Expense":{"id":"4504","number":"2026001","name":"sf-cli test — hosting","type":"invoice",
	    "created":"2024-03-01","due":"2024-03-31","variable":"2026001",
	    "amount":"49.00","amount_paid":"0.00","currency":"EUR"},
	  "Client":{"name":"Hosting s.r.o."},
	  "ExpenseItem":[{"quantity":"1.00000","unit_price":"49.0000","tax":"0.00","total":"49.0000"}],
	  "ExpensePayment":[]}`

	paidExpense = `{
	  "Expense":{"id":"4503","number":"2026000","name":"Domain renewal","type":"invoice",
	    "created":"2024-02-10","due":"2024-02-24","variable":"2026000",
	    "amount":"12.00","amount_paid":"12.00","currency":"EUR"},
	  "Client":{"name":"Registrar a.s."},
	  "ExpenseItem":[{"quantity":"1.00000","unit_price":"12.0000","tax":"0.00","total":"12.0000"}],
	  "ExpensePayment":[{"created":"2024-02-20 00:00:00","payment_type":"transfer","amount":"12.00"}]}`

	clientList = `{"itemCount":3,"pageCount":1,"items":[
	  {"Client":{"id":"7","name":"Acme s.r.o.","ico":"46655034","ic_dph":"SK2023513470",
	    "city":"Bratislava","email":"faktury@acme.sk","address":"Hlavná 1","zip":"81101",
	    "country":"Slovakia","phone":"+421900123456","iban":"SK3112000000198742637541"}},
	  {"Client":{"id":"8","name":"Beta a.s.","ico":"12345678","city":"Košice","email":"info@beta.sk"}},
	  {"Client":{"id":"9","name":"Gamma s.r.o.","ico":"87654321","city":"Žilina","email":"ucto@gamma.sk"}}]}`
)

func invoiceList(unpaidOnly bool) string {
	rows := []string{unpaidInvoice, paidInvoice, partialInvoice}
	if unpaidOnly {
		rows = []string{unpaidInvoice, partialInvoice}
	}
	return list(rows)
}

func expenseList(unpaidOnly bool) string {
	rows := []string{unpaidExpense, paidExpense}
	if unpaidOnly {
		rows = []string{unpaidExpense}
	}
	return list(rows)
}

func list(rows []string) string {
	return fmt.Sprintf(`{"itemCount":%d,"pageCount":1,"items":[%s]}`,
		len(rows), strings.Join(rows, ","))
}
