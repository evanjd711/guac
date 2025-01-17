package main

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/evanjd711/guac"
	"github.com/sirupsen/logrus"
)

var (
	certPath    string
	certKeyPath string
	guacdAddr   = "127.0.0.1:4822"
)

func main() {
	logrus.SetLevel(logrus.DebugLevel)

	if os.Getenv("CERT_PATH") != "" {
		certPath = os.Getenv("CERT_PATH")
	}

	if os.Getenv("CERT_KEY_PATH") != "" {
		certKeyPath = os.Getenv("CERT_KEY_PATH")
	}

	if certPath != "" && certKeyPath == "" {
		logrus.Fatal("You must set the CERT_KEY_PATH environment variable to specify the full path to the certificate keyfile")
	}

	if certPath == "" && certKeyPath != "" {
		logrus.Fatal("You must set the CERT_PATH environment variable to specify the full path to the certificate file")
	}

	if os.Getenv("GUACD_ADDRESS") != "" {
		guacdAddr = os.Getenv("GUACD_ADDRESS")
	}

	servlet := guac.NewServer(DemoDoConnect)
	wsServer := guac.NewWebsocketServer(DemoDoConnect)

	sessions := guac.NewMemorySessionStore()
	wsServer.OnConnect = sessions.Add
	wsServer.OnDisconnect = sessions.Delete

	mux := http.NewServeMux()
	mux.Handle("/tunnel", servlet)
	mux.Handle("/tunnel/", servlet)
	mux.Handle("/websocket-tunnel", wsServer)
	mux.HandleFunc("/sessions/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		sessions.RLock()
		defer sessions.RUnlock()

		type ConnIds struct {
			Uuid string `json:"uuid"`
			Num  int    `json:"num"`
		}

		connIds := make([]*ConnIds, len(sessions.ConnIds))

		i := 0
		for id, num := range sessions.ConnIds {
			connIds[i] = &ConnIds{
				Uuid: id,
				Num:  num,
			}
		}

		if err := json.NewEncoder(w).Encode(connIds); err != nil {
			logrus.Error(err)
		}
	})

	tlsCfg := tls.Config{}
	if certPath != "" {
		cert, err := tls.LoadX509KeyPair(certPath, certKeyPath)
		if err != nil {
			logrus.Fatalf("Unable to load certificate keypair: %s\n", err)
		}

		tlsCfg.MinVersion = tls.VersionTLS13
		tlsCfg.Certificates = []tls.Certificate{cert}
		tlsCfg.CurvePreferences = []tls.CurveID{
			tls.X25519,
			tls.CurveP256,
			tls.CurveP384,
		}
	}

	s := &http.Server{
		Addr:           "0.0.0.0:4567",
		Handler:        mux,
		ReadTimeout:    guac.SocketTimeout,
		WriteTimeout:   guac.SocketTimeout,
		MaxHeaderBytes: 1 << 20,
		TLSConfig:      &tlsCfg,
	}

	if certPath != "" {
		logrus.Println("Serving on https://0.0.0.0:4567")

		err := s.ListenAndServeTLS("", "")
		if err != nil {
			logrus.Fatal(err)
		}
	} else {
		logrus.Println("Serving on http://0.0.0.0:4567")

		err := s.ListenAndServe()
		if err != nil {
			logrus.Fatal(err)
		}
	}
}

// DemoDoConnect creates the tunnel to the remote machine (via guacd)
func DemoDoConnect(request *http.Request) (guac.Tunnel, error) {
	config := guac.NewGuacamoleConfiguration()

	var query url.Values
	if request.URL.RawQuery == "connect" {
		// http tunnel uses the body to pass parameters
		data, err := io.ReadAll(request.Body)
		if err != nil {
			logrus.Error("Failed to read body ", err)
			return nil, err
		}
		_ = request.Body.Close()
		queryString := string(data)
		query, err = url.ParseQuery(queryString)
		if err != nil {
			logrus.Error("Failed to parse body query ", err)
			return nil, err
		}
		logrus.Debugln("body:", queryString, query)
	} else {
		query = request.URL.Query()
	}

	config.Protocol = query.Get("scheme")
	config.Parameters = map[string]string{}
	for k, v := range query {
		config.Parameters[k] = v[0]
	}

	var err error
	if query.Get("width") != "" {
		config.OptimalScreenHeight, err = strconv.Atoi(query.Get("width"))
		if err != nil || config.OptimalScreenHeight == 0 {
			logrus.Error("Invalid height")
			config.OptimalScreenHeight = 600
		}
	}
	if query.Get("height") != "" {
		config.OptimalScreenWidth, err = strconv.Atoi(query.Get("height"))
		if err != nil || config.OptimalScreenWidth == 0 {
			logrus.Error("Invalid width")
			config.OptimalScreenWidth = 800
		}
	}
	config.AudioMimetypes = []string{"audio/L16", "rate=44100", "channels=2"}

	logrus.Debug("Connecting to guacd")
	addr, err := net.ResolveTCPAddr("tcp", guacdAddr)
	if err != nil {
		logrus.Errorln("error resolving guacd address", err)
		return nil, err
	}

	conn, err := net.DialTCP("tcp", nil, addr)
	if err != nil {
		logrus.Errorln("error while connecting to guacd", err)
		return nil, err
	}

	stream := guac.NewStream(conn, guac.SocketTimeout)

	logrus.Debug("Connected to guacd")
	if request.URL.Query().Get("uuid") != "" {
		config.ConnectionID = request.URL.Query().Get("uuid")
	}

	sanitisedCfg := config
	sanitisedCfg.Parameters["password"] = "********"
	logrus.Debugf("Starting handshake with %#v", sanitisedCfg)
	err = stream.Handshake(config)
	if err != nil {
		return nil, err
	}
	logrus.Debug("Socket configured")
	return guac.NewSimpleTunnel(stream), nil
}
