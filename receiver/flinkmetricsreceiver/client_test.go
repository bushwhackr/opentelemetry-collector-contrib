// Copyright  The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// nolint:errcheck
package flinkmetricsreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/flinkmetricsreceiver"

import (
	"context"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/config/configtls"
	"go.uber.org/zap"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/flinkmetricsreceiver/internal/models"
)

const (
	// filenames for api responses
	jobsIDs                 = "jobs_ids.json"
	jobsMetricValues        = "jobs_metric_values.json"
	jobsWithID              = "jobs_with_id.json"
	subtaskMetricValues     = "subtask_metric_values.json"
	vertices                = "vertices.json"
	jobmanagerMetricValues  = "jobmanager_metric_values.json"
	jobsOverview            = "jobs_overview.json"
	taskmanagerIds          = "taskmanager_ids.json"
	taskmanagerMetricValues = "taskmanager_metric_values.json"

	// regex for endpoint matching
	jobsWithIDRegex             = "^/jobs/[a-z0-9]+$"
	taskmanagerMetricNamesRegex = "^/taskmanagers/[a-z0-9.:-]+/metrics$"
	verticesRegex               = "^/jobs/[a-z0-9]+/vertices/[a-z0-9]+$"
	jobsMetricNamesRegex        = "^/jobs/[a-z0-9]+/metrics$"
	subtaskMetricNamesRegex     = "^/jobs/[a-z0-9]+/vertices/[a-z0-9]+/subtasks/[0-9]+/metrics$"
	taskmanagerIDsRegex         = "^/taskmanagers$"
	apiResponses                = "apiresponses"
)

func TestNewClient(t *testing.T) {
	testCase := []struct {
		desc        string
		cfg         *Config
		host        component.Host
		settings    component.TelemetrySettings
		logger      *zap.Logger
		expectError error
	}{
		{
			desc: "Invalid HTTP config",
			cfg: &Config{
				HTTPClientSettings: confighttp.HTTPClientSettings{
					Endpoint: defaultEndpoint,
					TLSSetting: configtls.TLSClientSetting{
						TLSSetting: configtls.TLSSetting{
							CAFile: "/non/existent",
						},
					},
				},
			},
			host:        componenttest.NewNopHost(),
			settings:    componenttest.NewNopTelemetrySettings(),
			logger:      zap.NewNop(),
			expectError: errors.New("failed to create HTTP Client"),
		},
		{
			desc: "Valid Configuration",
			cfg: &Config{
				HTTPClientSettings: confighttp.HTTPClientSettings{
					TLSSetting: configtls.TLSClientSetting{},
					Endpoint:   defaultEndpoint,
				},
			},
			host:        componenttest.NewNopHost(),
			settings:    componenttest.NewNopTelemetrySettings(),
			logger:      zap.NewNop(),
			expectError: nil,
		},
	}

	for _, tc := range testCase {
		t.Run(tc.desc, func(t *testing.T) {
			ac, err := newClient(tc.cfg, tc.host, tc.settings, tc.logger)
			if tc.expectError != nil {
				require.Nil(t, ac)
				require.Contains(t, err.Error(), tc.expectError.Error())
			} else {
				require.NoError(t, err)

				actualClient, ok := ac.(*flinkClient)
				require.True(t, ok)

				require.Equal(t, tc.cfg.Endpoint, actualClient.hostEndpoint)
				require.Equal(t, tc.logger, actualClient.logger)
				require.NotNil(t, actualClient.client)
			}
		})
	}
}

func createTestClient(t *testing.T, baseEndpoint string) client {
	t.Helper()
	cfg := createDefaultConfig().(*Config)
	cfg.Endpoint = baseEndpoint

	testClient, err := newClient(cfg, componenttest.NewNopHost(), componenttest.NewNopTelemetrySettings(), zap.NewNop())
	require.NoError(t, err)
	return testClient
}

func TestGetJobmanagerMetrics(t *testing.T) {
	testCases := []struct {
		desc     string
		testFunc func(*testing.T)
	}{
		{
			desc: "Non-200 Response",
			testFunc: func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				metrics, err := tc.GetJobmanagerMetrics(context.Background())
				require.Nil(t, metrics)
				require.EqualError(t, err, "non 200 code returned 401")
			},
		},
		{
			desc: "Bad payload returned",
			testFunc: func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, err := w.Write([]byte("{"))
					require.NoError(t, err)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				metrics, err := tc.GetJobmanagerMetrics(context.Background())
				require.Nil(t, metrics)
				require.Contains(t, err.Error(), "failed to unmarshal response body")
			},
		},
		{
			desc: "Successful call",
			testFunc: func(t *testing.T) {
				jobmanagerMetricValuesData := loadAPIResponseData(t, apiResponses, jobmanagerMetricValues)
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, err := w.Write(jobmanagerMetricValuesData)
					require.NoError(t, err)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				// Load the valid data into a struct to compare
				var expected *models.MetricsResponse
				err := json.Unmarshal(jobmanagerMetricValuesData, &expected)
				require.NoError(t, err)

				actual, err := tc.GetJobmanagerMetrics(context.Background())
				require.NoError(t, err)
				require.Equal(t, expected, &actual.Metrics)

				hostname, err := os.Hostname()
				require.Nil(t, err)
				require.EqualValues(t, hostname, actual.Host)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, tc.testFunc)
	}
}

func TestGetTaskmanagersMetrics(t *testing.T) {
	testCases := []struct {
		desc     string
		testFunc func(*testing.T)
	}{
		{
			desc: "Non-200 Response",
			testFunc: func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				metrics, err := tc.GetTaskmanagersMetrics(context.Background())
				require.Nil(t, metrics)
				require.EqualError(t, err, "non 200 code returned 401")
			},
		},
		{
			desc: "Bad taskmanagers payload returned",
			testFunc: func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, err := w.Write([]byte(`{`))
					require.NoError(t, err)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				metrics, err := tc.GetTaskmanagersMetrics(context.Background())
				require.Nil(t, metrics)
				require.Contains(t, err.Error(), "failed to unmarshal response body:")
			},
		},
		{
			desc: "Bad taskmanagers metrics payload returned",
			testFunc: func(t *testing.T) {
				taskmanagerIDs := loadAPIResponseData(t, apiResponses, taskmanagerIds)
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if match, _ := regexp.MatchString(taskmanagerIDsRegex, r.URL.Path); match {
						_, err := w.Write(taskmanagerIDs)
						require.NoError(t, err)
						return
					}

					_, err := w.Write([]byte("{"))
					require.NoError(t, err)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				metrics, err := tc.GetTaskmanagersMetrics(context.Background())
				require.Nil(t, metrics)
				require.Contains(t, err.Error(), "failed to unmarshal response body:")
			},
		},
		{
			desc: "Successful call",
			testFunc: func(t *testing.T) {
				taskmanagerIDs := loadAPIResponseData(t, apiResponses, taskmanagerIds)
				taskmanagerMetricValuesData := loadAPIResponseData(t, apiResponses, taskmanagerMetricValues)
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if match, _ := regexp.MatchString(taskmanagerIDsRegex, r.URL.Path); match {
						_, err := w.Write(taskmanagerIDs)
						require.NoError(t, err)
						return
					}

					if match, _ := regexp.MatchString(taskmanagerMetricNamesRegex, r.URL.Path); match {
						_, err := w.Write(taskmanagerMetricValuesData)
						require.NoError(t, err)
						return
					}
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				// Load the valid data into a struct to compare
				var expected *models.MetricsResponse
				err := json.Unmarshal(taskmanagerMetricValuesData, &expected)
				require.NoError(t, err)

				actual, err := tc.GetTaskmanagersMetrics(context.Background())
				require.NoError(t, err)
				require.Len(t, actual, 1)
				require.Equal(t, expected, &actual[0].Metrics)
				require.EqualValues(t, "172.26.0.3", actual[0].Host)
				require.EqualValues(t, "172.26.0.3:34457-7b2520", actual[0].TaskmanagerID)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, tc.testFunc)
	}
}

func TestGetJobsMetrics(t *testing.T) {
	testCases := []struct {
		desc     string
		testFunc func(*testing.T)
	}{
		{
			desc: "Non-200 Response",
			testFunc: func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				metrics, err := tc.GetJobsMetrics(context.Background())
				require.Nil(t, metrics)
				require.EqualError(t, err, "non 200 code returned 401")
			},
		},
		{
			desc: "Bad payload returned",
			testFunc: func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, err := w.Write([]byte(`{`))
					require.NoError(t, err)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				metrics, err := tc.GetJobsMetrics(context.Background())
				require.Nil(t, metrics)
				require.Contains(t, err.Error(), "failed to unmarshal response body")
			},
		},
		{
			desc: "bad payload returned call",
			testFunc: func(t *testing.T) {
				jobsOverviewData := loadAPIResponseData(t, apiResponses, jobsOverview)
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == jobsOverviewEndpoint {
						_, err := w.Write(jobsOverviewData)
						require.NoError(t, err)
						return
					}
					_, err := w.Write([]byte(`{`))
					require.NoError(t, err)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				metrics, err := tc.GetJobsMetrics(context.Background())
				require.Nil(t, metrics)
				require.Contains(t, err.Error(), "failed to unmarshal response body")
			},
		},
		{
			desc: "Successful call",
			testFunc: func(t *testing.T) {
				jobsOverviewData := loadAPIResponseData(t, apiResponses, jobsOverview)
				jobsMetricValuesData := loadAPIResponseData(t, apiResponses, jobsMetricValues)
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == jobsOverviewEndpoint {
						_, err := w.Write(jobsOverviewData)
						require.NoError(t, err)
						return
					}
					if match, _ := regexp.MatchString(jobsMetricNamesRegex, r.URL.Path); match {
						_, err := w.Write(jobsMetricValuesData)
						require.NoError(t, err)
						return
					}
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				// Load the valid data into a struct to compare
				var expected *models.MetricsResponse
				err := json.Unmarshal(jobsMetricValuesData, &expected)
				require.NoError(t, err)

				actual, err := tc.GetJobsMetrics(context.Background())
				require.NoError(t, err)
				require.Len(t, actual, 1)
				require.Equal(t, expected, &actual[0].Metrics)
				require.EqualValues(t, "State machine job", actual[0].JobName)

				hostname, err := os.Hostname()
				require.Nil(t, err)
				require.EqualValues(t, hostname, actual[0].Host)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, tc.testFunc)
	}
}

func TestGetSubtasksMetrics(t *testing.T) {
	testCases := []struct {
		desc     string
		testFunc func(*testing.T)
	}{
		{
			desc: "Non-200 Response",
			testFunc: func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				metrics, err := tc.GetSubtasksMetrics(context.Background())
				require.Nil(t, metrics)
				require.EqualError(t, err, "non 200 code returned 401")
			},
		},
		{
			desc: "Bad payload returned",
			testFunc: func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, err := w.Write([]byte("{"))
					require.NoError(t, err)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				metrics, err := tc.GetSubtasksMetrics(context.Background())
				require.Nil(t, metrics)
				require.Contains(t, err.Error(), "failed to unmarshal response body")
			},
		},
		{
			desc: "Bad payload jobs IDs returned",
			testFunc: func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					jobsData := loadAPIResponseData(t, apiResponses, jobsIDs)
					if r.URL.Path == jobsEndpoint {
						_, err := w.Write(jobsData)
						require.NoError(t, err)
						return
					}
					_, err := w.Write([]byte("{"))
					require.NoError(t, err)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				metrics, err := tc.GetSubtasksMetrics(context.Background())
				require.Nil(t, metrics)
				require.Contains(t, err.Error(), "failed to unmarshal response body")
			},
		},
		{
			desc: "Bad payload vertices IDs returned",
			testFunc: func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					jobsData := loadAPIResponseData(t, apiResponses, jobsIDs)
					jobsWithIDData := loadAPIResponseData(t, apiResponses, jobsWithID)
					if r.URL.Path == jobsEndpoint {
						_, err := w.Write(jobsData)
						require.NoError(t, err)
						return
					}
					if match, _ := regexp.MatchString(jobsWithIDRegex, r.URL.Path); match {
						_, err := w.Write(jobsWithIDData)
						require.NoError(t, err)
						return
					}
					_, err := w.Write([]byte("{"))
					require.NoError(t, err)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				metrics, err := tc.GetSubtasksMetrics(context.Background())
				require.Nil(t, metrics)
				require.Contains(t, err.Error(), "failed to unmarshal response body")
			},
		},
		{
			desc: "Bad payload subtask metrics returned",
			testFunc: func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					jobsData := loadAPIResponseData(t, apiResponses, jobsIDs)
					jobsWithIDData := loadAPIResponseData(t, apiResponses, jobsWithID)
					verticesData := loadAPIResponseData(t, apiResponses, vertices)
					if r.URL.Path == jobsEndpoint {
						_, err := w.Write(jobsData)
						require.NoError(t, err)
						return
					}
					if match, _ := regexp.MatchString(jobsWithIDRegex, r.URL.Path); match {
						_, err := w.Write(jobsWithIDData)
						require.NoError(t, err)
						return
					}
					if match, _ := regexp.MatchString(verticesRegex, r.URL.Path); match {
						_, err := w.Write(verticesData)
						require.NoError(t, err)
						return
					}
					_, err := w.Write([]byte("{"))
					require.NoError(t, err)
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				metrics, err := tc.GetSubtasksMetrics(context.Background())
				require.Nil(t, metrics)
				require.Contains(t, err.Error(), "failed to unmarshal response body")
			},
		},
		{
			desc: "Successful call",
			testFunc: func(t *testing.T) {
				jobsData := loadAPIResponseData(t, apiResponses, jobsIDs)
				jobsWithIDData := loadAPIResponseData(t, apiResponses, jobsWithID)
				verticesData := loadAPIResponseData(t, apiResponses, vertices)
				subtaskMetricValuesData := loadAPIResponseData(t, apiResponses, subtaskMetricValues)
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == jobsEndpoint {
						_, err := w.Write(jobsData)
						require.NoError(t, err)
						return
					}
					if match, _ := regexp.MatchString(jobsWithIDRegex, r.URL.Path); match {
						_, err := w.Write(jobsWithIDData)
						require.NoError(t, err)
						return
					}
					if match, _ := regexp.MatchString(verticesRegex, r.URL.Path); match {
						_, err := w.Write(verticesData)
						require.NoError(t, err)
						return
					}
					if match, _ := regexp.MatchString(subtaskMetricNamesRegex, r.URL.Path); match {
						_, err := w.Write(subtaskMetricValuesData)
						require.NoError(t, err)
						return
					}
				}))
				defer ts.Close()

				tc := createTestClient(t, ts.URL)

				var e *models.JobsResponse
				_ = json.Unmarshal(jobsData, &e)
				require.EqualValues(t, e.Jobs[0].ID, "54a5c6e527e00e1bb861272a39fe13e4")

				// Load the valid data into a struct to compare
				var expected *models.MetricsResponse
				err := json.Unmarshal(subtaskMetricValuesData, &expected)
				require.NoError(t, err)

				actual, err := tc.GetSubtasksMetrics(context.Background())
				require.NoError(t, err)
				require.Len(t, actual, 2)
				require.Equal(t, expected, &actual[0].Metrics)
				require.EqualValues(t, "State machine job", actual[0].JobName)
				require.EqualValues(t, "172.26.0.3", actual[0].Host)
				// require.EqualValues(t, "flink-worker", actual[0].Host)
				require.EqualValues(t, "172.26.0.3:34457-7b2520", actual[0].TaskmanagerID)
				require.EqualValues(t, "Source: Custom Source", actual[0].TaskName)
				require.EqualValues(t, "0", actual[0].SubtaskIndex)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, tc.testFunc)
	}
}

func loadAPIResponseData(t *testing.T, folder, fileName string) []byte {
	t.Helper()
	fullPath := filepath.Join("testdata", folder, fileName)

	data, err := ioutil.ReadFile(fullPath)
	require.NoError(t, err)

	return data
}
