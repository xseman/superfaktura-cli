//go:build ignore

// A stand-in for the SuperFaktura API that the demo tapes record against, so a
// recording spends no quota, shows nobody's accounting and comes out the same
// every time. It keeps its records in memory: an invoice created in a tape is
// in the next list, and a payment turns it paid.
//
// It answers only the paths the tapes use, in the shapes the live API has (see
// the fixture server in internal/tui/frame_test.go). Anything else is a 404
// and a line in the log, which is how a tape that wandered off shows up.
//
//	go run docs/demo/api.go -addr 127.0.0.1:8484
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type item struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Quantity     string  `json:"quantity"`
	UnitPrice    float64 `json:"unit_price"`
	Tax          float64 `json:"tax"`
	ItemPriceVAT float64 `json:"item_price_vat"`
}

type payment struct {
	ID          string `json:"id"`
	Created     string `json:"created"`
	PaymentType string `json:"payment_type"`
	Amount      string `json:"amount"`
}

type clientRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	ICO     string `json:"ico"`
	ICDPH   string `json:"ic_dph,omitempty"`
	Address string `json:"address"`
	City    string `json:"city"`
	Zip     string `json:"zip"`
	Country string `json:"country"`
	Email   string `json:"email"`
}

type invoice struct {
	ID          string  `json:"id"`
	Number      string  `json:"invoice_no_formatted"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Status      string  `json:"status"`
	Created     string  `json:"created"`
	Delivery    string  `json:"delivery"`
	Due         string  `json:"due"`
	Paydate     *string `json:"paydate"`
	Variable    string  `json:"variable"`
	Amount      string  `json:"amount"`
	VAT         string  `json:"vat"`
	Total       string  `json:"total_amount"`
	AmountPaid  string  `json:"amount_paid"`
	Currency    string  `json:"invoice_currency"`
	PaymentType string  `json:"payment_type"`
	Token       string  `json:"token"`

	client   *clientRecord
	items    []item
	payments []payment
}

type expense struct {
	ID         string `json:"id"`
	Number     string `json:"number"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Created    string `json:"created"`
	Due        string `json:"due"`
	Variable   string `json:"variable"`
	Amount     string `json:"amount"`
	AmountPaid string `json:"amount_paid"`
	Currency   string `json:"currency"`

	supplier string
}

type state struct {
	mu        sync.Mutex
	today     time.Time
	clients   []*clientRecord
	invoices  []*invoice
	expenses  []*expense
	nextID    int
	remaining int
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8484", "address to listen on")
	flag.Parse()

	s := seed(time.Now())
	log.Printf("demo API on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, s))
}

func (s *state) day(offset int) string {
	return s.today.AddDate(0, 0, offset).Format("2006-01-02")
}

func seed(now time.Time) *state {
	s := &state{
		today:     time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local),
		nextID:    309111,
		remaining: 912,
	}
	s.clients = []*clientRecord{
		{ID: "7", Name: "Acme s.r.o.", ICO: "46655034", ICDPH: "SK2023513470", Address: "Hlavná 1", City: "Bratislava", Zip: "81101", Country: "Slovensko", Email: "faktury@acme.sk"},
		{ID: "8", Name: "Beta a.s.", ICO: "35712345", ICDPH: "SK2020123456", Address: "Mlynská 12", City: "Košice", Zip: "04001", Country: "Slovensko", Email: "uctaren@beta.sk"},
		{ID: "9", Name: "Gamma s.r.o.", ICO: "50123987", Address: "Národná 8", City: "Žilina", Zip: "01001", Country: "Slovensko", Email: "info@gamma.sk"},
		{ID: "10", Name: "Kaviareň Pod Hradom s.r.o.", ICO: "52998811", Address: "Hradná 3", City: "Trenčín", Zip: "91101", Country: "Slovensko", Email: "kava@podhradom.sk"},
		{ID: "11", Name: "Modrý Most a.s.", ICO: "36554433", ICDPH: "SK2021998877", Address: "Štefánikova 44", City: "Nitra", Zip: "94901", Country: "Slovensko", Email: "fakturacia@modrymost.sk"},
		{ID: "12", Name: "Acme s.r.o. Trading", ICO: "53110022", Address: "Hlavná 1", City: "Bratislava", Zip: "81101", Country: "Slovensko", Email: "trading@acme.sk"},
	}

	type line struct {
		name       string
		qty, price float64
	}
	add := func(clientID string, created, due int, lines []line, paid float64) {
		inv := s.newInvoice(s.client(clientID), s.day(created), s.day(due), "transfer")
		for _, l := range lines {
			inv.items = append(inv.items, s.newItem(l.name, l.qty, l.price, 23))
		}
		s.total(inv)
		if paid != 0 {
			s.pay(inv, paid, s.day(created+4), "transfer")
		}
	}
	all := -1.0 // pay in full

	s.nextID = 309101
	add("7", -70, -56, []line{{"Konzultácie", 8, 75}, {"Cestovné", 1, 40}}, all)
	add("8", -52, -38, []line{{"Webhosting, ročne", 1, 118.80}}, all)
	add("9", -40, -26, []line{{"Vývoj e-shopu", 24, 60}}, 800)
	add("10", -35, -21, []line{{"Doména podhradom.sk", 1, 12}}, 0)
	add("11", -20, -6, []line{{"Audit prístupnosti", 1, 1200}}, 0)
	add("7", -12, 2, []line{{"Konzultácie", 6, 75}}, 0)
	add("8", -8, 6, []line{{"Správa serverov", 5, 45}}, 0)
	add("12", -5, 9, []line{{"Školenie Go", 2, 350}}, 0)
	add("9", -3, 11, []line{{"Údržba e-shopu", 10, 45}}, all)
	add("10", -1, 13, []line{{"Webstránka", 1, 2400}, {"Fotografie", 1, 180}}, 0)

	s.expenses = []*expense{
		{ID: "4507", Name: "Prenájom kancelárie", supplier: "Reality Centrum s.r.o.", Created: s.day(-2), Due: s.day(5), Amount: "450.00", AmountPaid: "0.00"},
		{ID: "4506", Name: "Účtovníctvo za mesiac", supplier: "Účto Plus s.r.o.", Created: s.day(-18), Due: s.day(-4), Amount: "120.00", AmountPaid: "0.00"},
		{ID: "4505", Name: "Softvérové licencie", supplier: "JetBrains s.r.o.", Created: s.day(-25), Due: s.day(-11), Amount: "89.00", AmountPaid: "89.00"},
		{ID: "4504", Name: "Hosting", supplier: "Websupport s.r.o.", Created: s.day(-33), Due: s.day(-19), Amount: "49.00", AmountPaid: "49.00"},
	}
	for i, e := range s.expenses {
		e.Number = fmt.Sprintf("%d%03d", s.today.Year(), 4-i)
		e.Variable, e.Type, e.Currency = e.Number, "invoice", "EUR"
		e.Status = "1"
		if e.AmountPaid == e.Amount {
			e.Status = "3"
		}
	}
	return s
}

func (s *state) client(id string) *clientRecord {
	for _, c := range s.clients {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (s *state) newInvoice(c *clientRecord, created, due, paymentType string) *invoice {
	id := strconv.Itoa(s.nextID)
	s.nextID++
	year, _ := strconv.Atoi(created[:4])
	count := 1
	for _, inv := range s.invoices {
		if strings.HasPrefix(inv.Number, strconv.Itoa(year)) {
			count++
		}
	}
	number := fmt.Sprintf("%d%03d", year, count)
	inv := &invoice{
		ID: id, Number: number, Name: "Faktúra " + number, Type: "regular",
		Created: created, Delivery: created, Due: due, Variable: number,
		Currency: "EUR", PaymentType: paymentType, Token: "demo" + id, client: c,
	}
	s.invoices = append([]*invoice{inv}, s.invoices...)
	return inv
}

func (s *state) newItem(name string, qty, price, tax float64) item {
	id := strconv.Itoa(804900 + s.nextID%1000*10 + len(name))
	return item{
		ID: id, Name: name, Quantity: fmt.Sprintf("%.5f", qty),
		UnitPrice: price, Tax: tax, ItemPriceVAT: round(qty * price * (1 + tax/100)),
	}
}

func (s *state) total(inv *invoice) {
	var net, vat float64
	for _, it := range inv.items {
		qty, _ := strconv.ParseFloat(it.Quantity, 64)
		net += qty * it.UnitPrice
		vat += qty * it.UnitPrice * it.Tax / 100
	}
	inv.Amount, inv.VAT, inv.Total = money(net), money(vat), money(net+vat)
	s.status(inv)
}

func (s *state) pay(inv *invoice, amount float64, date, paymentType string) payment {
	if amount <= 0 {
		amount = num(inv.Total) - num(inv.AmountPaid)
	}
	p := payment{
		ID:      strconv.Itoa(52000 + s.payments()),
		Created: date + " 00:00:00", PaymentType: paymentType, Amount: money(amount),
	}
	inv.payments = append(inv.payments, p)
	inv.AmountPaid = money(num(inv.AmountPaid) + amount)
	s.status(inv)
	if inv.Status == "3" {
		inv.Paydate = &date
	}
	return p
}

func (s *state) payments() int {
	n := 0
	for _, inv := range s.invoices {
		n += len(inv.payments)
	}
	return n
}

func (s *state) status(inv *invoice) {
	switch paid, total := num(inv.AmountPaid), num(inv.Total); {
	case paid >= total && total > 0:
		inv.Status = "3"
	case paid > 0:
		inv.Status = "2"
	default:
		inv.Status = "1"
	}
}

// record is an invoice as a list row carries it: complete, with the client,
// items and payments alongside.
func (inv *invoice) record() map[string]any {
	c := inv.client
	if c == nil {
		c = &clientRecord{}
	}
	return map[string]any{
		"Invoice": inv,
		"Client":  c,
		"0": map[string]string{
			"total":  fmt.Sprintf("%.6f", num(inv.Total)),
			"to_pay": fmt.Sprintf("%.6f", num(inv.Total)-num(inv.AmountPaid)),
		},
		"InvoiceItem":    orEmpty(inv.items),
		"InvoicePayment": orEmpty(inv.payments),
		"Tag":            []any{},
	}
}

func (e *expense) record() map[string]any {
	return map[string]any{
		"Expense":        e,
		"Client":         map[string]string{"name": e.supplier},
		"ExpenseItem":    []any{map[string]string{"quantity": "1.00000", "unit_price": e.Amount, "tax": "0.00", "total": e.Amount}},
		"ExpensePayment": []any{},
	}
}

func (s *state) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.remaining--
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-RateLimit-DailyLimit", "1000")
	h.Set("X-RateLimit-DailyRemaining", strconv.Itoa(s.remaining))
	h.Set("X-RateLimit-DailyReset", s.today.AddDate(0, 0, 1).Format("02.01.2006 15:04:05"))
	h.Set("X-RateLimit-MonthlyLimit", "30000")
	h.Set("X-RateLimit-MonthlyRemaining", strconv.Itoa(s.remaining+17000))
	h.Set("X-RateLimit-MonthlyReset", time.Date(s.today.Year(), s.today.Month()+1, 1, 0, 0, 0, 0, time.Local).Format("02.01.2006 15:04:05"))

	path, params := splitPath(r.URL.Path)
	log.Printf("%s %s %v", r.Method, path, params)

	switch {
	case path == "/invoices/index.json":
		s.listInvoices(w, params)
	case strings.HasPrefix(path, "/invoices/view/"):
		if inv := s.invoice(strings.TrimSuffix(strings.TrimPrefix(path, "/invoices/view/"), ".json")); inv != nil {
			write(w, inv.record())
		} else {
			fail(w, http.StatusNotFound, "Invoice not found")
		}
	case path == "/invoices/create":
		s.createInvoice(w, r)
	case path == "/invoice_payments/add/ajax:1/api:1":
		s.addPayment(w, r)
	case strings.HasPrefix(path, "/invoices/delete/"):
		id := strings.TrimPrefix(path, "/invoices/delete/")
		s.invoices = slices.DeleteFunc(s.invoices, func(inv *invoice) bool { return inv.ID == id })
		write(w, map[string]any{"error": 0, "error_message": "Faktúra bola zmazaná"})
	case strings.HasPrefix(path, "/invoices/mark_sent/"):
		write(w, map[string]any{"error": 0, "error_message": "Faktúra bola označená ako odoslaná"})
	case strings.HasPrefix(path, "/invoices/pdf/"):
		h.Set("Content-Type", "application/pdf")
		_, _ = io.WriteString(w, "%PDF-1.4\n% demo invoice\n%%EOF\n")
	case path == "/expenses/index.json":
		s.listExpenses(w, params)
	case path == "/clients/index.json":
		s.listClients(w, params)
	case path == "/clients/create":
		s.createClient(w, r)
	case path == "/tags/index.json":
		write(w, map[string]string{"3": "retainer", "4": "urgent"})
	case path == "/users/company_switcher":
		write(w, map[string]any{"companies": []any{map[string]any{"UserProfile": map[string]string{
			"id": "1001", "company_name": "Demo Studio s.r.o.", "ico": "12345678", "country_id": "191",
		}}}})
	default:
		fail(w, http.StatusNotFound, "the demo API does not answer "+path)
	}
}

// splitPath separates the CakePHP-style "key:value" segments from the path.
// The payment endpoint carries two of its own, which are part of its name.
func splitPath(raw string) (string, map[string]string) {
	if strings.HasPrefix(raw, "/invoice_payments/add") {
		return raw, map[string]string{}
	}
	params := map[string]string{}
	var kept []string
	for _, segment := range strings.Split(raw, "/") {
		if key, value, ok := strings.Cut(segment, ":"); ok {
			params[key] = value
			continue
		}
		kept = append(kept, segment)
	}
	return strings.Join(kept, "/"), params
}

func (s *state) invoice(id string) *invoice {
	for _, inv := range s.invoices {
		if inv.ID == id {
			return inv
		}
	}
	return nil
}

// matchesStatus applies the status filter: 1 issued, 2 partially paid, 3 paid,
// 99 overdue, and | for several at once.
func (s *state) matchesStatus(filter, status, due string) bool {
	if filter == "" {
		return true
	}
	for _, want := range strings.Split(filter, "|") {
		switch {
		case want == "99" && status != "3" && due < s.day(0):
			return true
		case want == status:
			return true
		}
	}
	return false
}

// matchesPeriod applies the created time filter the browser sends: 4 this
// month, 6 this year, 7 last year, 0 or nothing for all time.
func (s *state) matchesPeriod(filter, created string) bool {
	switch filter {
	case "4":
		return created[:7] == s.today.Format("2006-01")
	case "6":
		return created[:4] == strconv.Itoa(s.today.Year())
	case "7":
		return created[:4] == strconv.Itoa(s.today.Year()-1)
	}
	return true
}

func (s *state) listInvoices(w http.ResponseWriter, params map[string]string) {
	var rows []any
	for _, inv := range s.invoices {
		if !s.matchesStatus(params["status"], inv.Status, inv.Due) || !s.matchesPeriod(params["created"], inv.Created) {
			continue
		}
		if id := params["client_id"]; id != "" && (inv.client == nil || inv.client.ID != id) {
			continue
		}
		rows = append(rows, inv.record())
	}
	writeList(w, rows, params)
}

func (s *state) listExpenses(w http.ResponseWriter, params map[string]string) {
	var rows []any
	for _, e := range s.expenses {
		if s.matchesStatus(params["status"], e.Status, e.Due) && s.matchesPeriod(params["created"], e.Created) {
			rows = append(rows, e.record())
		}
	}
	writeList(w, rows, params)
}

func (s *state) listClients(w http.ResponseWriter, params map[string]string) {
	search := strings.ToLower(decodeSearch(params["search"]))
	clients := slices.Clone(s.clients)
	if params["sort"] == "name" {
		slices.SortFunc(clients, func(a, b *clientRecord) int { return strings.Compare(a.Name, b.Name) })
	}
	var rows []any
	for _, c := range clients {
		if search == "" || strings.Contains(strings.ToLower(c.Name), search) {
			rows = append(rows, map[string]any{"Client": c})
		}
	}
	writeList(w, rows, params)
}

func (s *state) createInvoice(w http.ResponseWriter, r *http.Request) {
	var doc struct {
		Invoice     map[string]any   `json:"Invoice"`
		Client      map[string]any   `json:"Client"`
		InvoiceItem []map[string]any `json:"InvoiceItem"`
	}
	if err := readPayload(r, &doc); err != nil {
		fail(w, http.StatusOK, err.Error())
		return
	}

	c := s.client(text(doc.Invoice["client_id"]))
	if c == nil && text(doc.Client["name"]) != "" {
		c = &clientRecord{ID: strconv.Itoa(len(s.clients) + 7), Name: text(doc.Client["name"]), Country: "Slovensko"}
		s.clients = append(s.clients, c)
	}
	if c == nil {
		write(w, map[string]any{"error": 1, "error_message": map[string]any{"client_id": []string{"Klient nepatrí pod túto firmu."}}})
		return
	}

	created := orDate(text(doc.Invoice["created"]), s.day(0))
	due := orDate(text(doc.Invoice["due"]), s.today.AddDate(0, 0, 14).Format("2006-01-02"))
	inv := s.newInvoice(c, created, due, orDefault(text(doc.Invoice["payment_type"]), "transfer"))
	if name := text(doc.Invoice["name"]); name != "" {
		inv.Name = name
	}
	for _, it := range doc.InvoiceItem {
		inv.items = append(inv.items, s.newItem(text(it["name"]), orOne(num(text(it["quantity"]))), num(text(it["unit_price"])), num(text(it["tax"]))))
	}
	s.total(inv)
	write(w, map[string]any{"error": 0, "error_message": "Faktúra bola vytvorená", "data": inv.record()})
}

func (s *state) addPayment(w http.ResponseWriter, r *http.Request) {
	var doc struct {
		InvoicePayment map[string]any `json:"InvoicePayment"`
	}
	if err := readPayload(r, &doc); err != nil {
		fail(w, http.StatusOK, err.Error())
		return
	}
	inv := s.invoice(text(doc.InvoicePayment["invoice_id"]))
	if inv == nil {
		fail(w, http.StatusOK, "Invoice ID not found.")
		return
	}
	p := s.pay(inv, num(text(doc.InvoicePayment["amount"])),
		orDate(text(doc.InvoicePayment["date"]), s.day(0)),
		orDefault(text(doc.InvoicePayment["payment_type"]), "transfer"))
	write(w, map[string]any{"error": 0, "error_message": "Úhrada bola pridaná", "data": map[string]any{
		"InvoicePayment": p, "Invoice": inv,
	}})
}

func (s *state) createClient(w http.ResponseWriter, r *http.Request) {
	var doc struct {
		Client clientRecord `json:"Client"`
	}
	if err := readPayload(r, &doc); err != nil {
		fail(w, http.StatusOK, err.Error())
		return
	}
	c := doc.Client
	c.ID = strconv.Itoa(len(s.clients) + 7)
	s.clients = append(s.clients, &c)
	write(w, map[string]any{"error": 0, "error_message": "Klient bol uložený", "data": map[string]any{"Client": c}})
}

// readPayload accepts both encodings the CLI sends: a form field "data"
// holding JSON, and a raw JSON body.
func readPayload(r *http.Request, into any) error {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return json.NewDecoder(r.Body).Decode(into)
	}
	if err := r.ParseForm(); err != nil {
		return err
	}
	return json.Unmarshal([]byte(r.PostFormValue("data")), into)
}

func writeList(w http.ResponseWriter, rows []any, params map[string]string) {
	perPage, _ := strconv.Atoi(params["per_page"])
	page, _ := strconv.Atoi(params["page"])
	if perPage <= 0 {
		perPage = 25
	}
	page = max(page, 1)
	pages := max((len(rows)+perPage-1)/perPage, 1)
	from := min((page-1)*perPage, len(rows))
	to := min(from+perPage, len(rows))
	write(w, map[string]any{"itemCount": len(rows), "pageCount": pages, "items": orEmpty(rows[from:to])})
}

func write(w http.ResponseWriter, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func fail(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	write(w, map[string]any{"error": 1, "message": message})
}

func decodeSearch(value string) string {
	value = strings.NewReplacer("-", "+", "_", "/", ",", "=").Replace(value)
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return ""
	}
	return string(decoded)
}

func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

// orDate keeps a date the server could read, the way strtotime would for the
// plain forms; anything relative falls back.
func orDate(value, fallback string) string {
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t.Format("2006-01-02")
	}
	if strings.HasPrefix(value, "+") {
		if days, err := strconv.Atoi(strings.Fields(value[1:])[0]); err == nil {
			return time.Now().AddDate(0, 0, days).Format("2006-01-02")
		}
	}
	return fallback
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func orOne(v float64) float64 {
	if v == 0 {
		return 1
	}
	return v
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func num(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func money(v float64) string { return fmt.Sprintf("%.2f", v) }

func round(v float64) float64 { return float64(int(v*100+0.5)) / 100 }
