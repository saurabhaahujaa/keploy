package grpcserver

// this will be the server file for the grpc connection

import (
	"context"
	"errors"
	"net"

	// "fmt"
	// "net/http"
	"strconv"
	// "strings"
	"time"

	"github.com/google/uuid"
	"go.keploy.io/server/graph"
	proto "go.keploy.io/server/grpc/regression"
	"go.keploy.io/server/pkg/models"
	regression2 "go.keploy.io/server/pkg/service/regression"
	"go.keploy.io/server/pkg/service/run"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

type Server struct {
	logger *zap.Logger
	svc    regression2.Service
	run    run.Service
	proto.UnimplementedRegressionServiceServer
}

func New(logger *zap.Logger, svc regression2.Service, run run.Service, listener net.Listener) error {

	// create an instance for grpc server
	srv := grpc.NewServer()
	proto.RegisterRegressionServiceServer(srv, &Server{logger: logger, svc: svc, run: run})
	reflection.Register(srv)
	err := srv.Serve(listener)
	return err

}

func (srv *Server) End(ctx context.Context, request *proto.EndRequest) (*proto.EndResponse, error) {
	stat := run.TestRunStatusFailed
	id := request.Id
	if request.Status == "true" {
		stat = run.TestRunStatusPassed
	}
	now := time.Now().Unix()
	err := srv.run.Put(ctx, run.TestRun{
		ID:      id,
		Updated: now,
		Status:  stat,
	})
	if err != nil {
		return &proto.EndResponse{Message: err.Error()}, nil
	}
	return &proto.EndResponse{Message: "OK"}, nil
}

func (srv *Server) Start(ctx context.Context, request *proto.StartRequest) (*proto.StartResponse, error) {
	t := request.Total
	total, err := strconv.Atoi(t)
	if err != nil {
		return nil, err
	}
	app := request.App
	if app == "" {
		return nil, errors.New("app is required in request")
	}
	id := uuid.New().String()
	now := time.Now().Unix()
	err = srv.run.Put(ctx, run.TestRun{
		ID:      id,
		Created: now,
		Updated: now,
		Status:  run.TestRunStatusRunning,
		CID:     graph.DEFAULT_COMPANY,
		App:     app,
		User:    graph.DEFAULT_USER,
		Total:   total,
	})
	if err != nil {
		return nil, err
	}
	return &proto.StartResponse{Id: id}, nil
}

// map[string]*StrArr --> map[string][]string
func getStringMap(m map[string]*proto.StrArr) map[string][]string {
	res := map[string][]string{}
	for k, v := range m {
		res[k] = v.Value
	}
	return res
}

func getProtoMap(m map[string][]string) map[string]*proto.StrArr {
	res := map[string]*proto.StrArr{}
	for k, v := range m {
		arr := &proto.StrArr{}
		arr.Value = append(arr.Value, v...)
		res[k] = arr
	}
	return res
}
func getProtoTC(tcs models.TestCase) (*proto.TestCase, error) {
	reqHeader := getProtoMap(map[string][]string(tcs.HttpReq.Header))
	respHeader := getProtoMap(map[string][]string(tcs.HttpResp.Header))
	deps := []*proto.Dependency{}
	allKeys := getProtoMap(map[string][]string(tcs.AllKeys))
	anchors := getProtoMap(map[string][]string(tcs.Anchors))
	for _, j := range tcs.Deps {
		data := []*proto.DataBytes{}
		for _, k := range j.Data {
			data = append(data, &proto.DataBytes{
				Bin: k,
			})
		}
		deps = append(deps, &proto.Dependency{
			Name: j.Name,
			Type: string(j.Type),
			Meta: j.Meta,
			Data: data,
		})
	}
	ptcs := &proto.TestCase{
		Id:       tcs.ID,
		Created:  tcs.Created,
		Updated:  tcs.Updated,
		Captured: tcs.Captured,
		CID:      tcs.CID,
		AppID:    tcs.AppID,
		URI:      tcs.URI,
		HttpReq: &proto.HttpReq{
			Method:     string(tcs.HttpReq.Method),
			ProtoMajor: int64(tcs.HttpReq.ProtoMajor),
			ProtoMinor: int64(tcs.HttpReq.ProtoMinor),
			URL:        tcs.HttpReq.URL,
			URLParams:  tcs.HttpReq.URLParams,
			Header:     reqHeader,
			Body:       tcs.HttpReq.Body,
		},
		HttpResp: &proto.HttpResp{
			StatusCode: int64(tcs.HttpResp.StatusCode),
			Header:     respHeader,
			Body:       tcs.HttpResp.Body,
		},
		Deps:    deps,
		AllKeys: allKeys,
		Anchors: anchors,
		Noise:   tcs.Noise,
	}
	return ptcs, nil
}

func (srv *Server) GetTC(ctx context.Context, request *proto.GetTCRequest) (*proto.TestCase, error) {
	id := request.Id
	app := request.App
	tcs, err := srv.svc.Get(ctx, graph.DEFAULT_COMPANY, app, id)
	if err != nil {
		return nil, err
	}
	// print(tcs)
	tcs, err = srv.svc.Get(ctx, graph.DEFAULT_COMPANY, app, id)
	if err != nil {
		return nil, err
	}
	ptcs, err := getProtoTC(tcs)
	if err != nil {
		return nil, err
	}
	return ptcs, nil
}

func (srv *Server) GetTCS(ctx context.Context, request *proto.GetTCSRequest) (*proto.GetTCSResponse, error) {
	app := request.App
	if app == "" {
		return nil, errors.New("app is required in request")
	}
	offsetStr := request.Offset
	limitStr := request.Limit
	var (
		offset int
		limit  int
		err    error
	)
	if offsetStr != "" {
		offset, err = strconv.Atoi(offsetStr)
		if err != nil {
			srv.logger.Error("request for fetching testcases in converting offset to integer")
		}
	}
	if limitStr != "" {
		limit, err = strconv.Atoi(limitStr)
		if err != nil {
			srv.logger.Error("request for fetching testcases in converting limit to integer")
		}
	}
	tcs, err := srv.svc.GetAll(ctx, graph.DEFAULT_COMPANY, app, &offset, &limit)
	if err != nil {
		return nil, err
	}
	var ptcs []*proto.TestCase
	for i := 0; i < len(tcs); i++ {
		ptc, err := getProtoTC(tcs[i])
		if err != nil {
			return nil, err
		}
		ptcs = append(ptcs, ptc)
	}
	return &proto.GetTCSResponse{Tcs: ptcs}, nil
}

func getHttpHeader(m map[string]*proto.StrArr) map[string][]string {
	res := map[string][]string{}
	for k, v := range m {
		res[k] = v.Value
	}
	return res
}

func (srv *Server) PostTC(ctx context.Context, request *proto.TestCaseReq) (*proto.PostTCResponse, error) {
	deps := []models.Dependency{}
	for _, j := range request.Dependency {
		data := [][]byte{}
		for _, k := range j.Data {
			data = append(data, k.Bin)
		}
		deps = append(deps, models.Dependency{
			Name: j.Name,
			Type: models.DependencyType(j.Type),
			Meta: j.Meta,
			Data: data,
		})
	}
	now := time.Now().UTC().Unix()
	inserted, err := srv.svc.Put(ctx, graph.DEFAULT_COMPANY, []models.TestCase{{
		ID:       uuid.New().String(),
		Created:  now,
		Updated:  now,
		Captured: request.Captured,
		URI:      request.URI,
		AppID:    request.AppID,
		HttpReq: models.HttpReq{
			Method:     models.Method(request.HttpReq.Method),
			ProtoMajor: int(request.HttpReq.ProtoMajor),
			ProtoMinor: int(request.HttpReq.ProtoMinor),
			URL:        request.HttpReq.URL,
			URLParams:  request.HttpReq.URLParams,
			Body:       request.HttpReq.Body,
			Header:     getHttpHeader(request.HttpReq.Header),
		},
		HttpResp: models.HttpResp{
			StatusCode: int(request.HttpResp.StatusCode),
			Body:       request.HttpResp.Body,
			Header:     getHttpHeader(request.HttpResp.Header),
		},
		Deps: deps,
	}})
	if err != nil {
		srv.logger.Error("error putting testcase", zap.Error(err))
		return nil, err
	}
	if len(inserted) == 0 {
		srv.logger.Error("unknown failure while inserting testcase")
		return nil, err
	}
	return &proto.PostTCResponse{
		TcsId: map[string]string{"id": inserted[0]},
	}, nil
}

func (srv *Server) DeNoise(ctx context.Context, request *proto.TestReq) (*proto.DeNoiseResponse, error) {
	err := srv.svc.DeNoise(ctx, graph.DEFAULT_COMPANY, request.ID, request.AppID, request.Resp.Body, getStringMap(request.Resp.Header))
	if err != nil {
		return &proto.DeNoiseResponse{Message: err.Error()}, nil
	}
	return &proto.DeNoiseResponse{Message: "OK"}, nil
}

func (srv *Server) Test(ctx context.Context, request *proto.TestReq) (*proto.TestResponse, error) {
	pass, err := srv.svc.Test(ctx, graph.DEFAULT_COMPANY, request.AppID, request.RunID, request.ID, models.HttpResp{
		StatusCode: int(request.Resp.StatusCode),
		Header:     getStringMap(request.Resp.Header),
		Body:       request.Resp.Body,
	})
	if err != nil {
		return nil, err
	}
	return &proto.TestResponse{
		Pass: map[string]bool{"pass": pass},
	}, nil
}
