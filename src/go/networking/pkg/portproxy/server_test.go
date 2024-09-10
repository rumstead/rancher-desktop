/*
Copyright © 2024 SUSE LLC
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package portproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"

	"github.com/docker/go-connections/nat"
	"github.com/rancher-sandbox/rancher-desktop/src/go/guestagent/pkg/types"
	"github.com/rancher-sandbox/rancher-desktop/src/go/networking/pkg/portproxy"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/nettest"
)

func TestNewPortProxy(t *testing.T) {
	logrus.SetLevel(logrus.DebugLevel)

	expectedResponse := "called the upstream server"

	testServerIP, err := availableIP()
	require.NoError(t, err, "cannot continue with the test since there are no available IP addresses")

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:", testServerIP))
	require.NoError(t, err)
	defer listener.Close()

	testServer := http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, expectedResponse)
		}),
	}
	defer testServer.Close()
	testServer.SetKeepAlivesEnabled(false)
	go testServer.Serve(listener)

	_, testPort, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)

	localListener, err := nettest.NewLocalListener("unix")
	require.NoError(t, err)
	defer localListener.Close()

	portProxy := portproxy.NewPortProxy(localListener, testServerIP)
	go portProxy.Start()

	getURL := fmt.Sprintf("http://localhost:%s", testPort)
	resp, err := httpGetRequest(context.Background(), getURL)
	require.ErrorIsf(t, err, syscall.ECONNREFUSED, "no listener should be available for port: %s", testPort)
	if resp != nil {
		resp.Body.Close()
	}

	port, err := nat.NewPort("tcp", testPort)
	require.NoError(t, err)

	portMapping := types.PortMapping{
		Remove: false,
		Ports: nat.PortMap{
			port: []nat.PortBinding{
				{
					HostIP:   testServerIP,
					HostPort: testPort,
				},
			},
		},
	}
	err = marshalAndSend(localListener, portMapping)
	require.NoError(t, err)

	resp, err = httpGetRequest(context.Background(), getURL)
	require.NoError(t, err)
	require.Equal(t, resp.StatusCode, http.StatusOK)
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, string(bodyBytes), expectedResponse)

	portMapping = types.PortMapping{
		Remove: true,
		Ports: nat.PortMap{
			port: []nat.PortBinding{
				{
					HostIP:   testServerIP,
					HostPort: testPort,
				},
			},
		},
	}
	err = marshalAndSend(localListener, portMapping)
	require.NoError(t, err)

	resp, err = httpGetRequest(context.Background(), getURL)
	require.Errorf(t, err, "the listener for port: %s should already be closed", testPort)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)
	if resp != nil {
		resp.Body.Close()
	}

	testServer.Close()
	portProxy.Close()
}

func httpGetRequest(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func marshalAndSend(listener net.Listener, portMapping types.PortMapping) error {
	b, err := json.Marshal(portMapping)
	if err != nil {
		return err
	}
	c, err := net.Dial(listener.Addr().Network(), listener.Addr().String())
	if err != nil {
		return err
	}
	_, err = c.Write(b)
	if err != nil {
		return err
	}
	return c.Close()
}

func availableIP() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue // interface down
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue // loopback interface
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return "", err
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue // not an ipv4 address
			}
			return ip.String(), nil
		}
	}
	return "", errors.New("are you connected to the network?")
}
