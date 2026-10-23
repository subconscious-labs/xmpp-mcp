package main

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"mellium.im/sasl"
	"mellium.im/xmpp"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/muc"
	"mellium.im/xmpp/mux"
	"mellium.im/xmpp/stanza"
	"mellium.im/xmlstream"
)

// version is overridden at build time via:
//
//	go build -ldflags "-X main.version=$(VERSION)" .
//
// Default value is used when building without ldflags (e.g. `go run .`, `go test`).
var version = "dev"

// ===== CONFIG =====

type Config struct {
	JID      jid.JID
	Password string
	Nick     string
	Users    map[string]bool
	MUCs     map[string]bool
}

func loadConfig() Config {
	jidStr := os.Getenv("XMPP_JID")
	pass := os.Getenv("XMPP_PASSWORD")
	if jidStr == "" || pass == "" {
		log.Fatal("XMPP_JID and XMPP_PASSWORD are required")
	}
	j, err := jid.Parse(jidStr)
	if err != nil {
		log.Fatalf("invalid XMPP_JID: %v", err)
	}
	nick := os.Getenv("XMPP_NICK")
	if nick == "" {
		nick = "mcp-bot"
	}
	return Config{
		JID:      j,
		Password: pass,
		Nick:     nick,
		Users:    parseList(os.Getenv("XMPP_WHITELIST_USERS")),
		MUCs:     parseList(os.Getenv("XMPP_WHITELIST_MUCS")),
	}
}

func parseList(s string) map[string]bool {
	m := make(map[string]bool)
	for _, v := range strings.Split(s, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			m[v] = true
		}
	}
	return m
}

// Буфер для сообщений, пришедших до инициализации MCP-клиента
type pendingBuffer struct {
	mu   sync.Mutex
	msgs []IncomingMessage
	cap  int
}

func (b *pendingBuffer) add(m IncomingMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.msgs) >= b.cap {
		b.msgs = b.msgs[1:] // выкидываем самое старое
	}
	b.msgs = append(b.msgs, m)
}

func (b *pendingBuffer) drain() []IncomingMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.msgs
	b.msgs = nil
	return out
}

// ===== EVENTS =====

func formatText(m IncomingMessage) string {
	if m.Room != "" {
		return "[" + m.Room + "] " + m.From + ": " + m.Body
	}
	return m.From + ": " + m.Body
}

type IncomingMessage struct {
	From string
	Room string
	Body string
}

type outboundMsg struct {
	To, Body string
}

func main() {
	cfg := loadConfig()

	events := make(chan IncomingMessage, 100)
	outbox := make(chan outboundMsg, 100)
	pending := &pendingBuffer{cap: 100}

	go runXMPP(cfg, events, outbox)

	var activeSessions sync.Map
	hooks := &server.Hooks{}
	hooks.AddOnRegisterSession(func(ctx context.Context, sess server.ClientSession) {
		activeSessions.Store(sess.SessionID(), sess)
	})
	hooks.AddOnUnregisterSession(func(ctx context.Context, sess server.ClientSession) {
		activeSessions.Delete(sess.SessionID())
	})

	s := server.NewMCPServer(
		"xmpp-bridge", version,
		server.WithToolCapabilities(true),
		server.WithHooks(hooks),
	)

	s.AddTool(
		mcp.NewTool("send_message",
			mcp.WithDescription("Send an XMPP message"),
			mcp.WithString("to", mcp.Required(), mcp.Description("Recipient JID")),
			mcp.WithString("body", mcp.Required(), mcp.Description("Message text")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			to, err := req.RequireString("to")
			if err != nil {
				return nil, err
			}
			body, err := req.RequireString("body")
			if err != nil {
				return nil, err
			}
			if _, err := jid.Parse(to); err != nil {
				return mcp.NewToolResultError("invalid JID: " + err.Error()), nil
			}
			outbox <- outboundMsg{To: to, Body: body}
			return mcp.NewToolResultText("queued"), nil
		},
	)

	// Горутина разбора событий из XMPP
	go func() {
		for msg := range events {
			delivered := false
			activeSessions.Range(func(_, v any) bool {
				sess := v.(server.ClientSession)
				if !sess.Initialized() {
					return true // ещё не готов, пропускаем
				}
				params := map[string]any{
					"level": "info",
					"data":  formatText(msg),
					"from":  msg.From,
					"room":  msg.Room,
					"body":  msg.Body,
				}
				err := s.SendNotificationToSpecificClient(sess.SessionID(), "notifications/message", params)
				if err != nil {
					log.Printf("notify %s: %v (will buffer)", sess.SessionID(), err)
					return true
				}
				delivered = true
				return true
			})
			if !delivered {
				pending.add(msg)
				log.Printf("buffered message (no initialized session): %s", formatText(msg))
			}
		}
	}()

	// Отдельная горутина: раз в секунду проверяем, не появилась ли инициализированная сессия,
	// и разгружаем буфер
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for range ticker.C {
			activeSessions.Range(func(_, v any) bool {
				sess := v.(server.ClientSession)
				if !sess.Initialized() {
					return true
				}
				for _, msg := range pending.drain() {
					params := map[string]any{
						"level": "info",
						"data":  formatText(msg),
						"from":  msg.From,
						"room":  msg.Room,
						"body":  msg.Body,
					}
					if err := s.SendNotificationToSpecificClient(sess.SessionID(), "notifications/message", params); err != nil {
						log.Printf("flush notify %s: %v", sess.SessionID(), err)
						pending.add(msg) // вернуть в буфер при неудаче
					}
				}
				return true
			})
		}
	}()

	if err := server.ServeStdio(s); err != nil {
		log.Fatal(err)
	}
}

// ===== XMPP CLIENT =====

func runXMPP(cfg Config, events chan<- IncomingMessage, outbox <-chan outboundMsg) {
	for {
		if err := runXMPPOnce(cfg, events, outbox); err != nil {
			log.Printf("xmpp session ended: %v; reconnecting in 5s", err)
		} else {
			log.Println("xmpp session closed cleanly; reconnecting in 5s")
		}
		time.Sleep(5 * time.Second)
	}
}

func runXMPPOnce(cfg Config, events chan<- IncomingMessage, outbox <-chan outboundMsg) error {
	ctx := context.Background()

	mucClient := &muc.Client{}

	chatHandler := mux.MessageHandlerFunc(func(msg stanza.Message, t xmlstream.TokenReadEncoder) error {
		body := extractBody(t)
		if body != "" {
			handleMessage(cfg, msg.From.Bare().String(), "", body, events)
		}
		return nil
	})

	groupHandler := mux.MessageHandlerFunc(func(msg stanza.Message, t xmlstream.TokenReadEncoder) error {
		body := extractBody(t)
		if body != "" {
			room := msg.From.Bare().String()          // ojuvutif@muc.polyserv.xyz
			sender := msg.From.Resourcepart()         // npopov
			handleMessage(cfg, sender, room, body, events)
		}
		return nil
	})

	handler := mux.New(
		stanza.NSClient,
		mux.Message(stanza.ChatMessage, xml.Name{Local: "body"}, chatHandler),
		mux.Message(stanza.GroupChatMessage, xml.Name{Local: "body"}, groupHandler),
		muc.HandleClient(mucClient),
	)

	sess, err := xmpp.DialClientSession(
		ctx,
		cfg.JID,
		xmpp.BindResource(),
		xmpp.StartTLS(&tls.Config{ServerName: cfg.JID.Domain().String()}),
		xmpp.SASL("", cfg.Password, sasl.ScramSha1Plus, sasl.ScramSha1, sasl.Plain),
	)
	if err != nil {
		return err
	}
	defer sess.Close()

	// Стартуем Serve в горутине — без этого inbound stanzas не читаются.
	serveErr := make(chan error, 1)
	go func() { serveErr <- sess.Serve(handler) }()
	time.Sleep(200 * time.Millisecond)

	// === ГЛАВНЫЙ ФИКС: объявляем себя доступным ===
	// После этого Prosody перестаёт складывать сообщения в offline storage
	// и доставляет накопленную очередь (включая приглашения в MUC).
	if err := sess.Send(ctx, stanza.Presence{Type: stanza.AvailablePresence}.Wrap(nil)); err != nil {
		log.Printf("initial presence: %v", err)
	} else {
		log.Println("sent initial presence (available)")
	}
	time.Sleep(200 * time.Millisecond)

	// Входим в MUC-комнаты. Ник передаётся как ресурс JID.
	for room := range cfg.MUCs {
		rj, err := jid.Parse(room)
		if err != nil {
			log.Printf("bad MUC JID %q: %v", room, err)
			continue
		}
		rjNick, err := rj.WithResource(cfg.Nick)
		if err != nil {
			log.Printf("bad nick %q for room %s: %v", cfg.Nick, room, err)
			continue
		}
		if _, err := mucClient.Join(ctx, rjNick, sess, muc.MaxHistory(0)); err != nil {
			log.Printf("join %s: %v", room, err)
		} else {
			log.Printf("join request sent: %s as %s", room, cfg.Nick)
		}
	}

	// Отправка исходящих из инструмента send_message
	go func() {
		for out := range outbox {
			to, err := jid.Parse(out.To)
			if err != nil {
				log.Printf("outbox bad JID %q: %v", out.To, err)
				continue
			}
			msg := stanza.Message{
				To:   to,
				Type: stanza.ChatMessage,
			}
			bodyReader := xmlstream.Wrap(
				xmlstream.Token(xml.CharData(out.Body)),
				xml.StartElement{Name: xml.Name{Local: "body"}},
			)
			if err := sess.Send(ctx, msg.Wrap(bodyReader)); err != nil {
				log.Printf("send to %s: %v", out.To, err)
			}
		}
	}()

	if err := <-serveErr; err != nil {
		return err
	}
	return nil
}

func extractBody(t xmlstream.TokenReadEncoder) string {
	var body string
	d := xml.NewTokenDecoder(t)
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "body" {
			if err := d.DecodeElement(&body, &se); err != nil {
				return ""
			}
			break
		}
	}
	return body
}

func handleMessage(cfg Config, from, room, body string, events chan<- IncomingMessage) {
	if room != "" {
		if !cfg.MUCs[room] {
			return
		}
	} else {
		if !cfg.Users[from] {
			return
		}
	}

	log.Printf("NOTIFY: room=%q from=%q body=%q", room, from, body) // ← временно, для проверки

	select {
	case events <- IncomingMessage{From: from, Room: room, Body: body}:
	default:
		log.Println("event channel full; dropping")
	}
}
