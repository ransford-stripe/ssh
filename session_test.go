package ssh

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func (srv *Server) serveOnce(l net.Listener) error {
	srv.ensureHandlers()
	if err := srv.ensureHostSigner(); err != nil {
		return err
	}
	conn, e := l.Accept()
	if e != nil {
		return e
	}
	srv.ChannelHandlers = map[string]ChannelHandler{
		"session":      ChannelHandlerFunc(DefaultSessionHandler),
		"direct-tcpip": ChannelHandlerFunc(DirectTCPIPHandler),
	}
	srv.handleConn(conn)
	return nil
}

func newLocalListener() net.Listener {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if l, err = net.Listen("tcp6", "[::1]:0"); err != nil {
			panic(fmt.Sprintf("failed to listen on a port: %v", err))
		}
	}
	return l
}

func newClientSession(t *testing.T, addr string, config *gossh.ClientConfig) (*gossh.Session, *gossh.Client, func()) {
	if config == nil {
		config = &gossh.ClientConfig{
			User: "testuser",
			Auth: []gossh.AuthMethod{
				gossh.Password("testpass"),
			},
		}
	}
	if config.HostKeyCallback == nil {
		config.HostKeyCallback = gossh.InsecureIgnoreHostKey()
	}
	client, err := gossh.Dial("tcp", addr, config)
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	return session, client, func() {
		session.Close()
		client.Close()
	}
}

func newTestSession(t *testing.T, srv *Server, cfg *gossh.ClientConfig) (*gossh.Session, *gossh.Client, func()) {
	l := newLocalListener()
	go srv.serveOnce(l)
	return newClientSession(t, l.Addr().String(), cfg)
}

func TestStdout(t *testing.T) {
	t.Parallel()
	testBytes := []byte("Hello world\n")
	session, _, cleanup := newTestSession(t, &Server{
		Handler: func(s Session) {
			s.Write(testBytes)
		},
	}, nil)
	defer cleanup()
	var stdout bytes.Buffer
	session.Stdout = &stdout
	if err := session.Run(""); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdout.Bytes(), testBytes) {
		t.Fatalf("stdout = %#v; want %#v", stdout.Bytes(), testBytes)
	}
}

func TestStderr(t *testing.T) {
	t.Parallel()
	testBytes := []byte("Hello world\n")
	session, _, cleanup := newTestSession(t, &Server{
		Handler: func(s Session) {
			s.Stderr().Write(testBytes)
		},
	}, nil)
	defer cleanup()
	var stderr bytes.Buffer
	session.Stderr = &stderr
	if err := session.Run(""); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stderr.Bytes(), testBytes) {
		t.Fatalf("stderr = %#v; want %#v", stderr.Bytes(), testBytes)
	}
}

func TestStdin(t *testing.T) {
	t.Parallel()
	testBytes := []byte("Hello world\n")
	session, _, cleanup := newTestSession(t, &Server{
		Handler: func(s Session) {
			io.Copy(s, s) // stdin back into stdout
		},
	}, nil)
	defer cleanup()
	var stdout bytes.Buffer
	session.Stdout = &stdout
	session.Stdin = bytes.NewBuffer(testBytes)
	if err := session.Run(""); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdout.Bytes(), testBytes) {
		t.Fatalf("stdout = %#v; want %#v given stdin = %#v", stdout.Bytes(), testBytes, testBytes)
	}
}

func TestUser(t *testing.T) {
	t.Parallel()
	testUser := []byte("progrium")
	session, _, cleanup := newTestSession(t, &Server{
		Handler: func(s Session) {
			io.WriteString(s, s.User())
		},
	}, &gossh.ClientConfig{
		User: string(testUser),
	})
	defer cleanup()
	var stdout bytes.Buffer
	session.Stdout = &stdout
	if err := session.Run(""); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdout.Bytes(), testUser) {
		t.Fatalf("stdout = %#v; want %#v given user = %#v", stdout.Bytes(), testUser, string(testUser))
	}
}

func TestDefaultExitStatusZero(t *testing.T) {
	t.Parallel()
	session, _, cleanup := newTestSession(t, &Server{
		Handler: func(s Session) {
			// noop
		},
	}, nil)
	defer cleanup()
	err := session.Run("")
	if err != nil {
		t.Fatalf("expected nil but got %v", err)
	}
}

func TestExplicitExitStatusZero(t *testing.T) {
	t.Parallel()
	session, _, cleanup := newTestSession(t, &Server{
		Handler: func(s Session) {
			s.Exit(0)
		},
	}, nil)
	defer cleanup()
	err := session.Run("")
	if err != nil {
		t.Fatalf("expected nil but got %v", err)
	}
}

func TestExitStatusNonZero(t *testing.T) {
	t.Parallel()
	session, _, cleanup := newTestSession(t, &Server{
		Handler: func(s Session) {
			s.Exit(1)
		},
	}, nil)
	defer cleanup()
	err := session.Run("")
	e, ok := err.(*gossh.ExitError)
	if !ok {
		t.Fatalf("expected ExitError but got %T", err)
	}
	if e.ExitStatus() != 1 {
		t.Fatalf("exit-status = %#v; want %#v", e.ExitStatus(), 1)
	}
}

func TestPty(t *testing.T) {
	t.Parallel()
	term := "xterm"
	winWidth := 40
	winHeight := 80
	done := make(chan bool)
	session, _, cleanup := newTestSession(t, &Server{
		Handler: func(s Session) {
			ptyReq, _, isPty := s.Pty()
			if !isPty {
				t.Fatalf("expected pty but none requested")
			}
			if ptyReq.Term != term {
				t.Fatalf("expected term %#v but got %#v", term, ptyReq.Term)
			}
			if ptyReq.Window.Width != winWidth {
				t.Fatalf("expected window width %#v but got %#v", winWidth, ptyReq.Window.Width)
			}
			if ptyReq.Window.Height != winHeight {
				t.Fatalf("expected window height %#v but got %#v", winHeight, ptyReq.Window.Height)
			}
			close(done)
		},
	}, nil)
	defer cleanup()
	if err := session.RequestPty(term, winHeight, winWidth, gossh.TerminalModes{}); err != nil {
		t.Fatalf("expected nil but got %v", err)
	}
	if err := session.Shell(); err != nil {
		t.Fatalf("expected nil but got %v", err)
	}
	<-done
}

func TestPtyResize(t *testing.T) {
	t.Parallel()
	winch0 := Window{40, 80}
	winch1 := Window{80, 160}
	winch2 := Window{20, 40}
	winches := make(chan Window)
	done := make(chan bool)
	session, _, cleanup := newTestSession(t, &Server{
		Handler: func(s Session) {
			ptyReq, winCh, isPty := s.Pty()
			if !isPty {
				t.Fatalf("expected pty but none requested")
			}
			if ptyReq.Window != winch0 {
				t.Fatalf("expected window %#v but got %#v", winch0, ptyReq.Window)
			}
			for win := range winCh {
				winches <- win
			}
			close(done)
		},
	}, nil)
	defer cleanup()
	// winch0
	if err := session.RequestPty("xterm", winch0.Height, winch0.Width, gossh.TerminalModes{}); err != nil {
		t.Fatalf("expected nil but got %v", err)
	}
	if err := session.Shell(); err != nil {
		t.Fatalf("expected nil but got %v", err)
	}
	gotWinch := <-winches
	if gotWinch != winch0 {
		t.Fatalf("expected window %#v but got %#v", winch0, gotWinch)
	}
	// winch1
	winchMsg := struct{ w, h uint32 }{uint32(winch1.Width), uint32(winch1.Height)}
	ok, err := session.SendRequest("window-change", true, gossh.Marshal(&winchMsg))
	if err == nil && !ok {
		t.Fatalf("unexpected error or bad reply on send request")
	}
	gotWinch = <-winches
	if gotWinch != winch1 {
		t.Fatalf("expected window %#v but got %#v", winch1, gotWinch)
	}
	// winch2
	winchMsg = struct{ w, h uint32 }{uint32(winch2.Width), uint32(winch2.Height)}
	ok, err = session.SendRequest("window-change", true, gossh.Marshal(&winchMsg))
	if err == nil && !ok {
		t.Fatalf("unexpected error or bad reply on send request")
	}
	gotWinch = <-winches
	if gotWinch != winch2 {
		t.Fatalf("expected window %#v but got %#v", winch2, gotWinch)
	}
	session.Close()
	<-done
}

func TestSignals(t *testing.T) {
	t.Parallel()

	session, _, cleanup := newTestSession(t, &Server{
		Handler: func(s Session) {
			signals := make(chan Signal)
			s.Signals(signals)
			if sig := <-signals; sig != SIGINT {
				t.Fatalf("expected signal %v but got %v", SIGINT, sig)
			}
			exiter := make(chan bool)
			go func() {
				if sig := <-signals; sig == SIGKILL {
					close(exiter)
				}
			}()
			<-exiter
		},
	}, nil)
	defer cleanup()

	go func() {
		session.Signal(gossh.SIGINT)
		session.Signal(gossh.SIGKILL)
	}()

	err := session.Run("")
	if err != nil {
		t.Fatalf("expected nil but got %v", err)
	}
}
