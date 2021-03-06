package main

import (
	"context"
	"flag"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/runconduit/conduit/controller/api/proxy"
	common "github.com/runconduit/conduit/controller/gen/common"
	pb "github.com/runconduit/conduit/controller/gen/proxy/telemetry"
	"github.com/runconduit/conduit/controller/k8s"
	"github.com/runconduit/conduit/controller/util"
	"google.golang.org/grpc/codes"
	k8sV1 "k8s.io/client-go/pkg/api/v1"
)

/* A simple script for posting simulated telemetry data to the proxy api */

var (
	responseCodes = []codes.Code{
		codes.OK,
		codes.PermissionDenied,
		codes.Unavailable,
	}

	streamSummary = &pb.StreamSummary{
		BytesSent:  12345,
		DurationMs: 10,
		FramesSent: 4,
	}
	ports = []uint32{3333, 6262}
)

func randomPort() uint32 {
	return ports[rand.Intn(len(ports))]
}

func randomCount() uint32 {
	return uint32(rand.Int31n(100) + 1)
}

func randomLatencies(count uint32) (latencies []*pb.Latency) {
	for i := uint32(0); i < count; i++ {

		// The latency value with precision to 100µs.
		latencyValue := uint32(rand.Int31n(int32(time.Second/(time.Millisecond * 10))))
		latency := pb.Latency{
			Latency: latencyValue,
			Count: rand.Uint32(),
		}
		latencies = append(latencies, &latency)
	}
	return
}

func randomEos(count uint32) (eos []*pb.EosScope) {
	responseCodes := make(map[uint32]uint32)
	for i := uint32(0); i < count; i++ {
		responseCodes[randomResponseCode()] += 1
	}
	for code, streamCount := range responseCodes {
		eos = append(eos, &pb.EosScope{
			Ctx:     &pb.EosCtx{End: &pb.EosCtx_GrpcStatusCode{GrpcStatusCode: code}},
			Streams: streamSummaries(streamCount),
		})
	}
	return
}

func randomResponseCode() uint32 {
	return uint32(responseCodes[rand.Intn(len(responseCodes))])
}

func streamSummaries(count uint32) (summaries []*pb.StreamSummary) {
	for i := uint32(0); i < count; i++ {
		summaries = append(summaries, streamSummary)
	}
	return
}

func stringToIp(str string) *common.IPAddress {
	octets := make([]uint8, 0)
	for _, num := range strings.Split(str, ".") {
		oct, _ := strconv.Atoi(num)
		octets = append(octets, uint8(oct))
	}
	return util.IPV4(octets[0], octets[1], octets[2], octets[3])
}

func podIndexFunc(obj interface{}) ([]string, error) {
	return nil, nil
}

func randomPod(pods []*k8sV1.Pod, prvPodIp *common.IPAddress) *common.IPAddress {
	var podIp *common.IPAddress
	for {
		if podIp != nil {
			break
		}

		randomPod := pods[rand.Intn(len(pods))]
		podIp = stringToIp(randomPod.Status.PodIP)
		if prvPodIp != nil && podIp.GetIpv4() == prvPodIp.GetIpv4() {
			podIp = nil
		}
	}
	return podIp
}

func main() {
	rand.Seed(time.Now().UnixNano())

	addr := flag.String("addr", ":8086", "address of proxy api")
	requestCount := flag.Int("requests", 0, "number of api requests to make (default: infinite)")
	sleep := flag.Duration("sleep", time.Second, "time to sleep between requests")
	maxPods := flag.Int("max-pods", 0, "total number of pods to simulate (default unlimited)")
	kubeConfigPath := flag.String("kubeconfig", "", "path to kube config - required")
	flag.Parse()

	if len(flag.Args()) > 0 {
		log.Fatal("Unable to parse command line arguments")
		return
	}

	client, conn, err := proxy.NewTelemetryClient(*addr)
	if err != nil {
		log.Fatal(err.Error())
	}
	defer conn.Close()

	clientSet, err := k8s.NewClientSet(*kubeConfigPath)
	if err != nil {
		log.Fatal(err.Error())
	}

	pods, err := k8s.NewPodIndex(clientSet, podIndexFunc)
	if err != nil {
		log.Fatal(err.Error())
	}
	pods.Run()

	// required for pods.List() to work -> otherwise the list of pods returned is empty
	time.Sleep(2 * time.Second)
	podList, err := pods.List()
	if err != nil {
		log.Fatal(err.Error())
	}
	allPods := make([]*k8sV1.Pod, 0)
	for _, pod := range podList {
		if pod.Status.PodIP != "" && (*maxPods == 0 || len(allPods) < *maxPods) {
			allPods = append(allPods, pod)
		}
	}

	for i := 0; (*requestCount == 0) || (i < *requestCount); i++ {
		count := randomCount()
		sourceIp := randomPod(allPods, nil)
		targetIp := randomPod(allPods, sourceIp)
		req := &pb.ReportRequest{
			Process: &pb.Process{
				ScheduledInstance:  "hello-1mfa0",
				ScheduledNamespace: "people",
			},
			ClientTransports: []*pb.ClientTransport{},
			ServerTransports: []*pb.ServerTransport{},
			Proxy:            pb.ReportRequest_INBOUND,
			Requests: []*pb.RequestScope{
				&pb.RequestScope{
					Ctx: &pb.RequestCtx{
						SourceIp: sourceIp,
						TargetAddr: &common.TcpAddress{
							Ip:   targetIp,
							Port: randomPort(),
						},
						Authority: "world.greeting:7778",
						Method:    &common.HttpMethod{Type: &common.HttpMethod_Registered_{Registered: common.HttpMethod_GET}},
						Path:      "/World/Greeting",
					},
					Count: count,
					Responses: []*pb.ResponseScope{
						&pb.ResponseScope{
							Ctx: &pb.ResponseCtx{
								HttpStatusCode: http.StatusOK,
							},
							ResponseLatencies: randomLatencies(count),
							Ends:              randomEos(count),
						},
					},
				},
			},
		}

		_, err = client.Report(context.Background(), req)
		if err != nil {
			log.Fatal(err.Error())
		}
		time.Sleep(*sleep)
	}
}
