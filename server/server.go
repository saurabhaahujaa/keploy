package server

import (
	"log"
	"math/rand"
	"net"
	"net/http"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/go-chi/chi"
	"github.com/go-chi/cors"
	"github.com/kelseyhightower/envconfig"
	"github.com/keploy/go-sdk/integrations/kchi"
	"github.com/keploy/go-sdk/integrations/khttpclient"
	"github.com/keploy/go-sdk/integrations/kmongo"
	"github.com/keploy/go-sdk/keploy"
	"github.com/soheilhy/cmux"
	"go.keploy.io/server/graph"
	"go.keploy.io/server/graph/generated"
	"go.keploy.io/server/grpc/grpcserver"
	"go.keploy.io/server/http/regression"
	"go.keploy.io/server/pkg/platform/mgo"
	"go.keploy.io/server/pkg/platform/telemetry"
	regression2 "go.keploy.io/server/pkg/service/regression"
	"go.keploy.io/server/pkg/service/run"
	"go.keploy.io/server/web"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// const defaultPort = "8080"

type config struct {
	MongoURI        string `envconfig:"MONGO_URI" default:"mongodb://localhost:27017"`
	DB              string `envconfig:"DB" default:"keploy"`
	TestCaseTable   string `envconfig:"TEST_CASE_TABLE" default:"test-cases"`
	TestRunTable    string `envconfig:"TEST_RUN_TABLE" default:"test-runs"`
	TestTable       string `envconfig:"TEST_TABLE" default:"tests"`
	TelemetryTable  string `envconfig:"TELEMETRY_TABLE" default:"telemetry"`
	APIKey          string `envconfig:"API_KEY"`
	EnableDeDup     bool   `envconfig:"ENABLE_DEDUP" default:"false"`
	EnableTelemetry bool   `envconfig:"ENABLE_TELEMETRY" default:"true"`
}

func Server() *chi.Mux {

	rand.Seed(time.Now().UTC().UnixNano())

	logger, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}
	defer logger.Sync() // flushes buffer, if any

	var conf config
	err = envconfig.Process("keploy", &conf)
	if err != nil {
		logger.Error("failed to read/process configuration", zap.Error(err))
	}

	cl, err := mgo.New(conf.MongoURI)
	if err != nil {
		logger.Fatal("failed to create mgo db client", zap.Error(err))
	}

	db := cl.Database(conf.DB)

	tdb := mgo.NewTestCase(kmongo.NewCollection(db.Collection(conf.TestCaseTable)), logger)

	rdb := mgo.NewRun(kmongo.NewCollection(db.Collection(conf.TestRunTable)), kmongo.NewCollection(db.Collection(conf.TestTable)), logger)

	enabled := conf.EnableTelemetry
	analyticsConfig := telemetry.NewTelemetry(mgo.NewTelemetryDB(db, conf.TelemetryTable, enabled, logger), enabled, keploy.GetMode() == keploy.MODE_OFF, logger)

	client := http.Client{
		Transport: khttpclient.NewInterceptor(http.DefaultTransport),
	}

	regSrv := regression2.New(tdb, rdb, logger, conf.EnableDeDup, analyticsConfig, client)
	runSrv := run.New(rdb, tdb, logger, analyticsConfig, client)

	srv := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: graph.NewResolver(logger, runSrv, regSrv)}))

	// initialize the client serveri
	r := chi.NewRouter()

	port := "8081"

	k := keploy.New(keploy.Config{
		App: keploy.AppConfig{
			Name: "Keploy-Test-App",
			Port: port,
			Filter: keploy.Filter{
				UrlRegex: "^/api",
			},

			Timeout: 80 * time.Second,
		},

		Server: keploy.ServerConfig{
			LicenseKey: conf.APIKey,
			// URL: "http://localhost:8081/api",
		},
	})

	r.Use(kchi.ChiMiddlewareV5(k))

	r.Use(cors.Handler(cors.Options{

		AllowedOrigins:   []string{"*"},
		AllowCredentials: true,
		ExposedHeaders:   []string{"Link"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
	}))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	r.Handle("/*", web.Handler())

	// add api routes
	r.Route("/api", func(r chi.Router) {
		regression.New(r, logger, regSrv, runSrv)
		r.Handle("/", playground.Handler("keploy graphql backend", "/api/query"))
		r.Handle("/query", srv)
	})

	analyticsConfig.Ping(keploy.GetMode() == keploy.MODE_TEST)

	listener, err := net.Listen("tcp", ":8081")

	if err != nil {
		panic(err)
	}

	m := cmux.New(listener)
	grpcListener := m.MatchWithWriters(cmux.HTTP2MatchHeaderFieldSendSettings("content-type", "application/grpc"))

	httpListener := m.Match(cmux.HTTP1Fast())

	log.Println("connect to http://localhost:8081/ for GraphQL playground")

	g := new(errgroup.Group)
	g.Go(func() error { return grpcserver.New(logger, regSrv, runSrv, grpcListener) })

	g.Go(func() error {
		srv := http.Server{Handler: r}
		err := srv.Serve(httpListener)
		return err
	})
	g.Go(func() error { return m.Serve() })
	g.Wait()

	return r
}
