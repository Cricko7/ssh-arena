package gamev1

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type JsonEnvelope struct {
	Topic    string `json:"topic,omitempty"`
	JSON     string `json:"json,omitempty"`
	Sequence int64  `json:"sequence,omitempty"`
}

type PlayerBootstrapRequest struct {
	SSHUsername          string `json:"ssh_username,omitempty"`
	PublicKeyFingerprint string `json:"public_key_fingerprint,omitempty"`
	RemoteAddr           string `json:"remote_addr,omitempty"`
}

type PlayerBootstrapResponse struct {
	PlayerID      string `json:"player_id,omitempty"`
	Role          string `json:"role,omitempty"`
	Created       bool   `json:"created,omitempty"`
	BootstrapJSON string `json:"bootstrap_json,omitempty"`
}

type ActionRequest struct {
	RequestID   string            `json:"request_id,omitempty"`
	PlayerID    string            `json:"player_id,omitempty"`
	ActionID    string            `json:"action_id,omitempty"`
	PayloadJSON string            `json:"payload_json,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type ActionResponse struct {
	RequestID    string `json:"request_id,omitempty"`
	ActionID     string `json:"action_id,omitempty"`
	Status       string `json:"status,omitempty"`
	ResponseJSON string `json:"response_json,omitempty"`
}

type MarketStreamRequest struct {
	PlayerID         string   `json:"player_id,omitempty"`
	Symbols          []string `json:"symbols,omitempty"`
	IncludeOrderbook bool     `json:"include_orderbook,omitempty"`
	IncludeTrades    bool     `json:"include_trades,omitempty"`
	IncludePortfolio bool     `json:"include_portfolio,omitempty"`
	IncludeChat      bool     `json:"include_chat,omitempty"`
}

type ChatRequest struct {
	PlayerID string `json:"player_id,omitempty"`
	JSON     string `json:"json,omitempty"`
}

type ChartSubscriptionRequest struct {
	PlayerID       string `json:"player_id,omitempty"`
	Ticker         string `json:"ticker,omitempty"`
	HistoryLimit   uint32 `json:"history_limit,omitempty"`
	OrderbookDepth uint32 `json:"orderbook_depth,omitempty"`
}

type PriceChartTick struct {
	Ticker   string `json:"ticker,omitempty"`
	JSON     string `json:"json,omitempty"`
	Sequence int64  `json:"sequence,omitempty"`
}

type AccountServiceClient interface {
	EnsurePlayer(ctx context.Context, in *PlayerBootstrapRequest, opts ...grpc.CallOption) (*PlayerBootstrapResponse, error)
}

type accountServiceClient struct{ cc grpc.ClientConnInterface }

func NewAccountServiceClient(cc grpc.ClientConnInterface) AccountServiceClient {
	return &accountServiceClient{cc: cc}
}

func (c *accountServiceClient) EnsurePlayer(ctx context.Context, in *PlayerBootstrapRequest, opts ...grpc.CallOption) (*PlayerBootstrapResponse, error) {
	out := new(PlayerBootstrapResponse)
	err := c.cc.Invoke(ctx, "/game.v1.AccountService/EnsurePlayer", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

type AccountServiceServer interface {
	EnsurePlayer(context.Context, *PlayerBootstrapRequest) (*PlayerBootstrapResponse, error)
}

type UnimplementedAccountServiceServer struct{}

func (UnimplementedAccountServiceServer) EnsurePlayer(context.Context, *PlayerBootstrapRequest) (*PlayerBootstrapResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method EnsurePlayer not implemented")
}

func RegisterAccountServiceServer(s grpc.ServiceRegistrar, srv AccountServiceServer) {
	s.RegisterService(&AccountService_ServiceDesc, srv)
}

type GameServiceClient interface {
	ExecuteAction(ctx context.Context, in *ActionRequest, opts ...grpc.CallOption) (*ActionResponse, error)
	GetMarketStream(ctx context.Context, opts ...grpc.CallOption) (GameService_GetMarketStreamClient, error)
	SubscribeToChart(ctx context.Context, opts ...grpc.CallOption) (GameService_SubscribeToChartClient, error)
}

type gameServiceClient struct{ cc grpc.ClientConnInterface }

func NewGameServiceClient(cc grpc.ClientConnInterface) GameServiceClient {
	return &gameServiceClient{cc: cc}
}

func (c *gameServiceClient) ExecuteAction(ctx context.Context, in *ActionRequest, opts ...grpc.CallOption) (*ActionResponse, error) {
	out := new(ActionResponse)
	err := c.cc.Invoke(ctx, "/game.v1.GameService/ExecuteAction", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *gameServiceClient) GetMarketStream(ctx context.Context, opts ...grpc.CallOption) (GameService_GetMarketStreamClient, error) {
	stream, err := c.cc.NewStream(ctx, &GameService_ServiceDesc.Streams[0], "/game.v1.GameService/GetMarketStream", opts...)
	if err != nil {
		return nil, err
	}
	return &gameServiceGetMarketStreamClient{ClientStream: stream}, nil
}

func (c *gameServiceClient) SubscribeToChart(ctx context.Context, opts ...grpc.CallOption) (GameService_SubscribeToChartClient, error) {
	stream, err := c.cc.NewStream(ctx, &GameService_ServiceDesc.Streams[1], "/game.v1.GameService/SubscribeToChart", opts...)
	if err != nil {
		return nil, err
	}
	return &gameServiceSubscribeToChartClient{ClientStream: stream}, nil
}

type GameServiceServer interface {
	ExecuteAction(context.Context, *ActionRequest) (*ActionResponse, error)
	GetMarketStream(GameService_GetMarketStreamServer) error
	SubscribeToChart(GameService_SubscribeToChartServer) error
}

type UnimplementedGameServiceServer struct{}

func (UnimplementedGameServiceServer) ExecuteAction(context.Context, *ActionRequest) (*ActionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method ExecuteAction not implemented")
}
func (UnimplementedGameServiceServer) GetMarketStream(GameService_GetMarketStreamServer) error {
	return status.Error(codes.Unimplemented, "method GetMarketStream not implemented")
}
func (UnimplementedGameServiceServer) SubscribeToChart(GameService_SubscribeToChartServer) error {
	return status.Error(codes.Unimplemented, "method SubscribeToChart not implemented")
}

func RegisterGameServiceServer(s grpc.ServiceRegistrar, srv GameServiceServer) {
	s.RegisterService(&GameService_ServiceDesc, srv)
}

type GameService_GetMarketStreamClient interface {
	Send(*MarketStreamRequest) error
	Recv() (*JsonEnvelope, error)
	grpc.ClientStream
}

type gameServiceGetMarketStreamClient struct{ grpc.ClientStream }

func (x *gameServiceGetMarketStreamClient) Send(m *MarketStreamRequest) error {
	return x.ClientStream.SendMsg(m)
}
func (x *gameServiceGetMarketStreamClient) Recv() (*JsonEnvelope, error) {
	m := new(JsonEnvelope)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

type GameService_GetMarketStreamServer interface {
	Send(*JsonEnvelope) error
	Recv() (*MarketStreamRequest, error)
	grpc.ServerStream
}

type gameServiceGetMarketStreamServer struct{ grpc.ServerStream }

func (x *gameServiceGetMarketStreamServer) Send(m *JsonEnvelope) error {
	return x.ServerStream.SendMsg(m)
}
func (x *gameServiceGetMarketStreamServer) Recv() (*MarketStreamRequest, error) {
	m := new(MarketStreamRequest)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

type GameService_SubscribeToChartClient interface {
	Send(*ChartSubscriptionRequest) error
	Recv() (*PriceChartTick, error)
	grpc.ClientStream
}

type gameServiceSubscribeToChartClient struct{ grpc.ClientStream }

func (x *gameServiceSubscribeToChartClient) Send(m *ChartSubscriptionRequest) error {
	return x.ClientStream.SendMsg(m)
}
func (x *gameServiceSubscribeToChartClient) Recv() (*PriceChartTick, error) {
	m := new(PriceChartTick)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

type GameService_SubscribeToChartServer interface {
	Send(*PriceChartTick) error
	Recv() (*ChartSubscriptionRequest, error)
	grpc.ServerStream
}

type gameServiceSubscribeToChartServer struct{ grpc.ServerStream }

func (x *gameServiceSubscribeToChartServer) Send(m *PriceChartTick) error {
	return x.ServerStream.SendMsg(m)
}
func (x *gameServiceSubscribeToChartServer) Recv() (*ChartSubscriptionRequest, error) {
	m := new(ChartSubscriptionRequest)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

type ChatServiceClient interface {
	SendChat(ctx context.Context, in *ChatRequest, opts ...grpc.CallOption) (*JsonEnvelope, error)
	StreamChat(ctx context.Context, in *MarketStreamRequest, opts ...grpc.CallOption) (ChatService_StreamChatClient, error)
}

type chatServiceClient struct{ cc grpc.ClientConnInterface }

func NewChatServiceClient(cc grpc.ClientConnInterface) ChatServiceClient {
	return &chatServiceClient{cc: cc}
}

func (c *chatServiceClient) SendChat(ctx context.Context, in *ChatRequest, opts ...grpc.CallOption) (*JsonEnvelope, error) {
	out := new(JsonEnvelope)
	err := c.cc.Invoke(ctx, "/game.v1.ChatService/SendChat", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *chatServiceClient) StreamChat(ctx context.Context, in *MarketStreamRequest, opts ...grpc.CallOption) (ChatService_StreamChatClient, error) {
	stream, err := c.cc.NewStream(ctx, &ChatService_ServiceDesc.Streams[0], "/game.v1.ChatService/StreamChat", opts...)
	if err != nil {
		return nil, err
	}
	x := &chatServiceStreamChatClient{ClientStream: stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

type ChatServiceServer interface {
	SendChat(context.Context, *ChatRequest) (*JsonEnvelope, error)
	StreamChat(*MarketStreamRequest, ChatService_StreamChatServer) error
}

type UnimplementedChatServiceServer struct{}

func (UnimplementedChatServiceServer) SendChat(context.Context, *ChatRequest) (*JsonEnvelope, error) {
	return nil, status.Error(codes.Unimplemented, "method SendChat not implemented")
}
func (UnimplementedChatServiceServer) StreamChat(*MarketStreamRequest, ChatService_StreamChatServer) error {
	return status.Error(codes.Unimplemented, "method StreamChat not implemented")
}

func RegisterChatServiceServer(s grpc.ServiceRegistrar, srv ChatServiceServer) {
	s.RegisterService(&ChatService_ServiceDesc, srv)
}

type ChatService_StreamChatClient interface {
	Recv() (*JsonEnvelope, error)
	grpc.ClientStream
}

type chatServiceStreamChatClient struct{ grpc.ClientStream }

func (x *chatServiceStreamChatClient) Recv() (*JsonEnvelope, error) {
	m := new(JsonEnvelope)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

type ChatService_StreamChatServer interface {
	Send(*JsonEnvelope) error
	grpc.ServerStream
}

type chatServiceStreamChatServer struct{ grpc.ServerStream }

func (x *chatServiceStreamChatServer) Send(m *JsonEnvelope) error { return x.ServerStream.SendMsg(m) }

func _AccountService_EnsurePlayer_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(PlayerBootstrapRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AccountServiceServer).EnsurePlayer(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/game.v1.AccountService/EnsurePlayer"}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(AccountServiceServer).EnsurePlayer(ctx, req.(*PlayerBootstrapRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _GameService_ExecuteAction_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(ActionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(GameServiceServer).ExecuteAction(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/game.v1.GameService/ExecuteAction"}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(GameServiceServer).ExecuteAction(ctx, req.(*ActionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _GameService_GetMarketStream_Handler(srv any, stream grpc.ServerStream) error {
	return srv.(GameServiceServer).GetMarketStream(&gameServiceGetMarketStreamServer{ServerStream: stream})
}

func _GameService_SubscribeToChart_Handler(srv any, stream grpc.ServerStream) error {
	return srv.(GameServiceServer).SubscribeToChart(&gameServiceSubscribeToChartServer{ServerStream: stream})
}

func _ChatService_SendChat_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(ChatRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ChatServiceServer).SendChat(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/game.v1.ChatService/SendChat"}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(ChatServiceServer).SendChat(ctx, req.(*ChatRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ChatService_StreamChat_Handler(srv any, stream grpc.ServerStream) error {
	m := new(MarketStreamRequest)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(ChatServiceServer).StreamChat(m, &chatServiceStreamChatServer{ServerStream: stream})
}

var AccountService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "game.v1.AccountService",
	HandlerType: (*AccountServiceServer)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "EnsurePlayer",
		Handler:    _AccountService_EnsurePlayer_Handler,
	}},
	Streams:  []grpc.StreamDesc{},
	Metadata: "proto/game/v1/game.proto",
}

var GameService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "game.v1.GameService",
	HandlerType: (*GameServiceServer)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "ExecuteAction",
		Handler:    _GameService_ExecuteAction_Handler,
	}},
	Streams: []grpc.StreamDesc{
		{StreamName: "GetMarketStream", Handler: _GameService_GetMarketStream_Handler, ServerStreams: true, ClientStreams: true},
		{StreamName: "SubscribeToChart", Handler: _GameService_SubscribeToChart_Handler, ServerStreams: true, ClientStreams: true},
	},
	Metadata: "proto/game/v1/game.proto",
}

var ChatService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "game.v1.ChatService",
	HandlerType: (*ChatServiceServer)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "SendChat",
		Handler:    _ChatService_SendChat_Handler,
	}},
	Streams: []grpc.StreamDesc{{
		StreamName:    "StreamChat",
		Handler:       _ChatService_StreamChat_Handler,
		ServerStreams: true,
	}},
	Metadata: "proto/game/v1/game.proto",
}
