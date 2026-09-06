// turnbridge: TURN (RFC 6062 TCP allocations) to SOCKS5 bridge.
// One persistent allocation, event-driven blocking I/O, no busy loops.
package main

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	magic   = 0x2112A442
	hdrSize = 20
)

// STUN methods (request class)
const (
	mAllocate = 0x0003
	mRefresh  = 0x0004
	mPerms    = 0x0008
	mConnect  = 0x000A
	mConnBind = 0x000B
)

// attributes
const (
	aUsername = 0x0006
	aIntegrity = 0x0008
	aErrCode  = 0x0009
	aRealm    = 0x0014
	aNonce    = 0x0015
	aXorPeer  = 0x0012
	aLifetime = 0x000D
	aReqTrans = 0x0019
	aConnID   = 0x002A
)

// ---------- low-level STUN framing ----------

type stunMsg struct {
	typ   uint16
	id    [12]byte
	attrs []rawAttr
}

type rawAttr struct {
	typ uint16
	val []byte
}

func newTxid() [12]byte {
	var b [12]byte
	rand.Read(b[:])
	return b
}

func attr(t uint16, v []byte) []byte {
	pad := (4 - len(v)%4) % 4
	out := make([]byte, 4, 4+len(v)+pad)
	binary.BigEndian.PutUint16(out[0:], t)
	binary.BigEndian.PutUint16(out[2:], uint16(len(v)))
	out = append(out, v...)
	return append(out, make([]byte, pad)...)
}

func msgBytes(method uint16, id [12]byte, attrs [][]byte) (full []byte, body []byte) {
	bl := 0
	for _, a := range attrs {
		bl += len(a)
	}
	body = make([]byte, 0, bl)
	for _, a := range attrs {
		body = append(body, a...)
	}
	hdr := make([]byte, hdrSize)
	binary.BigEndian.PutUint16(hdr[0:], method)
	binary.BigEndian.PutUint16(hdr[2:], uint16(len(body)))
	binary.BigEndian.PutUint32(hdr[4:], magic)
	copy(hdr[8:], id[:])
	full = append(full, hdr...)
	full = append(full, body...)
	return
}

func readMsg(r io.Reader) (*stunMsg, error) {
	hdr := make([]byte, hdrSize)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	typ := binary.BigEndian.Uint16(hdr[0:])
	ln := binary.BigEndian.Uint16(hdr[2:])
	if binary.BigEndian.Uint32(hdr[4:]) != magic {
		return nil, errors.New("bad magic cookie")
	}
	var id [12]byte
	copy(id[:], hdr[8:])
	body := make([]byte, ln)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	m := &stunMsg{typ: typ, id: id}
	for i := 0; i+4 <= len(body); {
		at := binary.BigEndian.Uint16(body[i:])
		al := int(binary.BigEndian.Uint16(body[i+2:]))
		if i+4+al > len(body) {
			break
		}
		m.attrs = append(m.attrs, rawAttr{at, body[i+4 : i+4+al]})
		i += 4 + al + (4-al%4)%4
	}
	return m, nil
}

func xorAddrValue(ip net.IP, port uint16, id [12]byte) []byte {
	if ip4 := ip.To4(); ip4 != nil {
		v := make([]byte, 8)
		v[0], v[1] = 0x00, 0x01
		binary.BigEndian.PutUint16(v[2:], port^uint16(magic>>16))
		binary.BigEndian.PutUint32(v[4:], binary.BigEndian.Uint32(ip4)^magic)
		return v
	}
	ip6 := ip.To16()
	v := make([]byte, 20)
	v[0], v[1] = 0x00, 0x02
	binary.BigEndian.PutUint16(v[2:], port^uint16(magic>>16))
	var mask [16]byte
	binary.BigEndian.PutUint32(mask[0:], magic)
	copy(mask[4:], id[:])
	for i := 0; i < 16; i++ {
		v[4+i] = ip6[i] ^ mask[i]
	}
	return v
}

func findAttr(m *stunMsg, t uint16) []byte {
	for _, a := range m.attrs {
		if a.typ == t {
			return a.val
		}
	}
	return nil
}

func parseErr(m *stunMsg) (int, string, string) {
	v := findAttr(m, aErrCode)
	code := 0
	if v != nil && len(v) >= 4 {
		code = int(v[2]&0x07)*100 + int(v[3])
	}
	return code, string(findAttr(m, aRealm)), string(findAttr(m, aNonce))
}

// ---------- TURN session (single allocation over one TCP control conn) ----------

type session struct {
	srv      string
	conn     net.Conn
	user     string
	pass     string
	realm    string
	nonce    string
	lifetime time.Duration
	dead     atomic.Bool
	onDead   func()

	mu        sync.Mutex
	pending   map[[12]byte]chan *stunMsg
	closeOnce sync.Once
}

func (s *session) isClosed() bool { return s.dead.Load() }

func (s *session) close() {
	s.closeOnce.Do(func() {
		s.dead.Store(true)
		s.mu.Lock()
		for k, ch := range s.pending {
			ch <- nil
			delete(s.pending, k)
		}
		s.conn.Close()
		s.mu.Unlock()
		if s.onDead != nil {
			s.onDead()
		}
	})
}

func (s *session) reader() {
	for {
		m, err := readMsg(s.conn)
		if err != nil {
			s.close()
			return
		}
		if m.typ&0x0100 != 0 { // response class
			s.mu.Lock()
			ch, ok := s.pending[m.id]
			if ok {
				delete(s.pending, m.id)
			}
			s.mu.Unlock()
			if ok {
				select {
				case ch <- m:
				default:
				}
			}
		}
		// indications over TCP allocations are not expected; ignore
	}
}

func (s *session) transact(full []byte, id [12]byte, timeout time.Duration) (*stunMsg, error) {
	ch := make(chan *stunMsg, 1)
	s.mu.Lock()
	if s.dead.Load() {
		s.mu.Unlock()
		return nil, errors.New("session closed")
	}
	s.pending[id] = ch
	_, err := s.conn.Write(full)
	s.mu.Unlock()
	if err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}
	select {
	case m := <-ch:
		if m == nil {
			return nil, errors.New("session closed")
		}
		return m, nil
	case <-time.After(timeout):
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, errors.New("transaction timeout")
	}
}

// seal appends MESSAGE-INTEGRITY using long-term credentials.
func (s *session) seal(body []byte, id [12]byte, method uint16) []byte {
	key := md5.Sum([]byte(s.user + ":" + s.realm + ":" + s.pass))
	miVal := make([]byte, 20)
	hdr := make([]byte, hdrSize)
	binary.BigEndian.PutUint16(hdr[0:], method)
	binary.BigEndian.PutUint16(hdr[2:], uint16(len(body)+24))
	binary.BigEndian.PutUint32(hdr[4:], magic)
	copy(hdr[8:], id[:])
	mac := hmac.New(sha1.New, key[:])
	mac.Write(hdr)
	mac.Write(body)
	copy(miVal, mac.Sum(nil))
	out := append([]byte{}, hdr...)
	out = append(out, body...)
	return append(out, attr(aIntegrity, miVal)...)
}

// request sends a request (with one 401-auth retry if credentials are set).
func (s *session) request(method uint16, mkAttrs func(id [12]byte) [][]byte, timeout time.Duration) (*stunMsg, error) {
	id := newTxid()
	attrs := mkAttrs(id)
	full, body := msgBytes(method, id, attrs)
	if s.user != "" && s.realm != "" {
		attrs = append(attrs, attr(aUsername, []byte(s.user)), attr(aRealm, []byte(s.realm)), attr(aNonce, []byte(s.nonce)))
		full, body = msgBytes(method, id, attrs)
		full = s.seal(body, id, method)
	}
	resp, err := s.transact(full, id, timeout)
	if err != nil {
		return nil, err
	}
	if resp.typ == method|0x0110 {
		code, realm, nonce := parseErr(resp)
		if code == 401 && s.user != "" && s.realm == "" {
			s.realm, s.nonce = realm, nonce
			id2 := newTxid()
			attrs2 := mkAttrs(id2)
			attrs2 = append(attrs2, attr(aUsername, []byte(s.user)), attr(aRealm, []byte(s.realm)), attr(aNonce, []byte(s.nonce)))
			full2, body2 := msgBytes(method, id2, attrs2)
			full2 = s.seal(body2, id2, method)
			return s.transact(full2, id2, timeout)
		}
	}
	return resp, nil
}

func expectOK(resp *stunMsg, method uint16) error {
	if resp.typ == method|0x0100 {
		return nil
	}
	if resp.typ == method|0x0110 {
		code, _, _ := parseErr(resp)
		return fmt.Errorf("relay returned %d", code)
	}
	return fmt.Errorf("unexpected response type %#x", resp.typ)
}

func allocate(srv, user, pass string) (*session, error) {
	conn, err := net.DialTimeout("tcp", srv, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial relay: %w", err)
	}
	s := &session{srv: srv, conn: conn, user: user, pass: pass,
		pending: make(map[[12]byte]chan *stunMsg)}
	go s.reader()
	resp, err := s.request(mAllocate, func(id [12]byte) [][]byte {
		return [][]byte{attr(aReqTrans, []byte{6, 0, 0, 0})} // proto TCP
	}, 10*time.Second)
	if err != nil {
		s.close()
		return nil, err
	}
	if err := expectOK(resp, mAllocate); err != nil {
		s.close()
		return nil, fmt.Errorf("allocate: %w", err)
	}
	lt := 600 * time.Second
	if v := findAttr(resp, aLifetime); v != nil && len(v) == 4 {
		lt = time.Duration(binary.BigEndian.Uint32(v)) * time.Second
	}
	s.lifetime = lt
	go s.refreshLoop()
	return s, nil
}

func (s *session) refreshLoop() {
	iv := s.lifetime * 2 / 3
	if iv < 30*time.Second {
		iv = 30 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for range t.C {
		if s.isClosed() {
			return
		}
		resp, err := s.request(mRefresh, func(id [12]byte) [][]byte {
			v := make([]byte, 4)
			binary.BigEndian.PutUint32(v, uint32(s.lifetime/time.Second))
			return [][]byte{attr(aLifetime, v)}
		}, 10*time.Second)
		if err != nil || resp == nil || resp.typ == mRefresh|0x0110 {
			code := -1
			if resp != nil {
				code, _, _ = parseErr(resp)
			}
			logf("[turn] refresh failed (code=%d, err=%v); closing session", code, err)
			s.close()
			return
		}
	}
}

// dialPeer: CreatePermission + Connect on control conn, then ConnectionBind on
// a fresh TCP conn which becomes the raw pipe to the peer.
func (s *session) dialPeer(peer net.IP, port uint16) (net.Conn, error) {
	resp, err := s.request(mPerms, func(id [12]byte) [][]byte {
		return [][]byte{attr(aXorPeer, xorAddrValue(peer, port, id))}
	}, 8*time.Second)
	if err != nil {
		return nil, err
	}
	if err := expectOK(resp, mPerms); err != nil {
		return nil, fmt.Errorf("permission: %w", err)
	}
	resp, err = s.request(mConnect, func(id [12]byte) [][]byte {
		return [][]byte{attr(aXorPeer, xorAddrValue(peer, port, id))}
	}, 15*time.Second)
	if err != nil {
		return nil, err
	}
	if err := expectOK(resp, mConnect); err != nil {
		if code, _, _ := parseErr(resp); code == 446 {
			return nil, fmt.Errorf("connect: %w", errConnExists)
		}
		return nil, fmt.Errorf("connect: %w", err)
	}
	cid := findAttr(resp, aConnID)
	if cid == nil {
		return nil, errors.New("connect success without CONNECTION-ID")
	}
	d, err := net.DialTimeout("tcp", s.srv, 8*time.Second)
	if err != nil {
		return nil, err
	}
	tc, _ := d.(*net.TCPConn)
	if tc != nil {
		tc.SetNoDelay(true)
	}
	d.SetDeadline(time.Now().Add(10 * time.Second))
	id := newTxid()
	attrs := [][]byte{attr(aConnID, cid)}
	if s.user != "" && s.realm != "" {
		attrs = append(attrs, attr(aUsername, []byte(s.user)), attr(aRealm, []byte(s.realm)), attr(aNonce, []byte(s.nonce)))
	}
	full, body := msgBytes(mConnBind, id, attrs)
	if s.user != "" && s.realm != "" {
		full = s.seal(body, id, mConnBind)
	}
	if _, err := d.Write(full); err != nil {
		d.Close()
		return nil, err
	}
	resp, err = readMsg(d)
	if err != nil {
		d.Close()
		return nil, err
	}
	if resp.typ != mConnBind|0x0100 {
		d.Close()
		return nil, fmt.Errorf("connection-bind: %s", respErrText(resp, mConnBind))
	}
	d.SetDeadline(time.Time{})
	return d, nil
}

func respErrText(resp *stunMsg, method uint16) string {
	if resp.typ == method|0x0110 {
		code, _, _ := parseErr(resp)
		return fmt.Sprintf("relay error %d", code)
	}
	return fmt.Sprintf("unexpected type %#x", resp.typ)
}

// ---------- supervisor ----------

var errConnExists = errors.New("connection already exists on this allocation")

type bridge struct {
	srv    string
	user   string
	pass   string
	allocs int
	mu     sync.Mutex
	pool   []*session
	rr     int
}

func (b *bridge) ensureSlot(i int) *session {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pool) < b.allocs {
		b.pool = make([]*session, b.allocs)
	}
	if s := b.pool[i]; s != nil && !s.isClosed() {
		return s
	}
	s, err := allocate(b.srv, b.user, b.pass)
	if err != nil {
		logf("[turn] alloc slot %d: %v", i, err)
		return nil
	}
	log.Printf("[turn] allocation #%d live on %s (lifetime %s)", i, b.srv, s.lifetime)
	s.onDead = func() { logf("[turn] allocation #%d died", i) }
	b.pool[i] = s
	return s
}

// next picks the next live allocation slot, round-robin.
func (b *bridge) next() *session {
	b.mu.Lock()
	rr := b.rr
	b.rr = (b.rr + 1) % b.allocs
	b.mu.Unlock()
	return b.ensureSlot(rr)
}

// ---------- SOCKS5 ----------

var lg struct {
	mu      sync.Mutex
	last    time.Time
	dropped int
}

func logf(f string, v ...interface{}) {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	now := time.Now()
	if now.Sub(lg.last) < time.Second {
		lg.dropped++
		return
	}
	if lg.dropped > 0 {
		log.Printf("[..] (dropped %d log lines)", lg.dropped)
		lg.dropped = 0
	}
	lg.last = now
	log.Printf(f, v...)
}

func resolve(host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip, nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, err
	}
	for _, i := range ips {
		if i.To4() != nil {
			return i, nil
		}
	}
	if len(ips) > 0 {
		return ips[0], nil
	}
	return nil, errors.New("no address found")
}

func socksFail(c net.Conn) {
	c.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
}

func handleConn(c net.Conn, b *bridge) {
	defer c.Close()
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	c.SetDeadline(time.Now().Add(20 * time.Second))
	h := make([]byte, 2)
	if _, err := io.ReadFull(c, h); err != nil || h[0] != 5 {
		return
	}
	n := int(h[1])
	if n == 0 || n > 32 {
		return
	}
	mt := make([]byte, n)
	if _, err := io.ReadFull(c, mt); err != nil {
		return
	}
	c.Write([]byte{5, 0}) // no-auth
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return
	}
	if hdr[1] != 1 { // CONNECT only
		socksFail(c)
		return
	}
	var host string
	switch hdr[3] {
	case 1:
		b4 := make([]byte, 4)
		if _, err := io.ReadFull(c, b4); err != nil {
			return
		}
		host = net.IP(b4).String()
	case 3:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return
		}
		d := make([]byte, l[0])
		if _, err := io.ReadFull(c, d); err != nil {
			return
		}
		host = string(d)
	case 4:
		b16 := make([]byte, 16)
		if _, err := io.ReadFull(c, b16); err != nil {
			return
		}
		host = net.IP(b16).String()
	default:
		socksFail(c)
		return
	}
	pt := make([]byte, 2)
	if _, err := io.ReadFull(c, pt); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(pt)

	ip, err := resolve(host)
	if err != nil {
		logf("resolve %s: %v", host, err)
		socksFail(c)
		return
	}
	var d net.Conn
	var derr error
	for attempt := 0; attempt < 3; attempt++ {
		sess := b.next()
		if sess == nil {
			derr = errors.New("no allocation available")
			time.Sleep(200 * time.Millisecond)
			continue
		}
		d, derr = sess.dialPeer(ip, port)
		if derr == nil {
			break
		}
		if errors.Is(derr, errConnExists) {
			logf("446 on %s:%d, rotating allocation (attempt %d)", ip, port, attempt+1)
			time.Sleep(150 * time.Millisecond)
			continue
		}
		break
	}
	if d == nil {
		logf("peer %s:%d: %v", ip, port, derr)
		socksFail(c)
		return
	}
	defer d.Close()
	c.SetDeadline(time.Time{})
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	go func() {
		io.Copy(d, c)
		d.Close()
	}()
	io.Copy(c, d)
}

func socksServe(ln net.Listener, b *bridge, max int) {
	sem := make(chan struct{}, max)
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			handleConn(c, b)
		}()
	}
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8002", "socks5 listen address")
	srv := flag.String("server", "", "TURN server host:port")
	user := flag.String("u", "", "username (optional, long-term credentials)")
	pass := flag.String("p", "", "password (optional)")
	max := flag.Int("max", 64, "max concurrent client connections")
	allocs := flag.Int("allocs", 6, "number of pooled TURN allocations")
	flag.Parse()
	if *srv == "" {
		log.Fatal("-server required")
	}
	log.SetFlags(log.LstdFlags)
	b := &bridge{srv: *srv, user: *user, pass: *pass, allocs: *allocs}
	go func() { // warm-up / keep-alive supervisor
		for {
			for i := 0; i < b.allocs; i++ {
				b.ensureSlot(i)
			}
			time.Sleep(15 * time.Second)
		}
	}()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("turnbridge listening on %s -> TURN %s", *listen, *srv)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		ln.Close()
	}()
	socksServe(ln, b, *max)
}
