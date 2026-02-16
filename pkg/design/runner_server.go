package design

import . "goa.design/goa/v3/dsl"

//go:generate go run goa.design/goa/v3/cmd/goa@v3.24.3 gen github.com/forkbombeu/credimi-runner/pkg/design -o ..
//go:generate go run ../../scripts/generate_openapi_public.go

var _ = API("runner-server", func() {
	Title("Runner Server")
	Description("Credimi runner server API.")
	Version("1.0")
	Meta("openapi:operationId", "{service}.{method}(.{routeIndex})")
	Meta("openapi:example", "false")
	Meta("openapi:summary", "{path}")
	Server("runner-server", func() {
		Services("worker", "credimi", "mobile", "docs")
		Host("local", func() {
			URI("http://127.0.0.1:8050")
		})
	})
})

var APIError = Type("APIError", func() {
	ErrorName("name", String, "Error class name", func() {
		Meta("struct:tag:json", "name")
	})
	Attribute("status", Int, "HTTP status code", func() {
		Meta("struct:field:name", "Code")
		Meta("struct:tag:json", "status")
	})
	Attribute("error", String, "Error domain", func() {
		Meta("struct:field:name", "Domain")
		Meta("struct:tag:json", "error")
	})
	Attribute("reason", String, "Error reason")
	Attribute("message", String, "Error message")
	Required("name", "status", "error", "reason", "message")
})

var ProcessStartResult = ResultType("ProcessStartResult", func() {
	Attribute("status", String)
	Attribute("namespace", String)
	Required("status", "namespace")
})

var FetchApkAndActionResult = ResultType("FetchApkAndActionResult", func() {
	Attribute("apk_path", String)
	Attribute("version_id", String)
	Attribute("code", String)
	Required("apk_path", "version_id")
})

var TouchFingerprintResult = ResultType("TouchFingerprintResult", func() {
	Attribute("status", String)
	Required("status")
})

var _ = Service("runner", func() {
	Description("Internal shared error model.")

	Error("bad_request", APIError)
	Error("unauthorized", APIError)
	Error("bad_gateway", APIError)
	Error("internal_error", APIError)
})

var _ = Service("docs", func() {
	Description("Static API documentation.")
	Files("/", "pkg/server/docs/index.html")
	Files("/docs/openapi.yaml", "pkg/gen/http/openapi.yaml")
	Files("/docs/openapi3.yaml", "pkg/gen/http/openapi3.yaml")
	Files("/docs/openapi3-public.json", "pkg/gen/http/openapi3-public.json")
})

var _ = Service("worker", func() {
	Description("Worker process management.")

	Error("bad_request", APIError)
	Error("internal_error", APIError)

	Method("process_start", func() {
		Payload(func() {
			Attribute("namespace", String, func() {
				Example("")
			})
			Attribute("old_namespace", String, func() {
				Example("")
			})
			Required("namespace")
			Example(map[string]any{
				"namespace":     "",
				"old_namespace": "",
			})
		})
		Result(ProcessStartResult)

		Error("bad_request", APIError)
		Error("internal_error", APIError)

		HTTP(func() {
			POST("/worker/{namespace}")
			Param("namespace")
			Body(func() {
				Attribute("old_namespace")
			})
			Response(StatusAccepted)
			Response("bad_request", StatusBadRequest)
			Response("internal_error", StatusInternalServerError)
		})
	})

	Method("process_list", func() {
		Result(ArrayOf(String))

		Error("internal_error", APIError)

		HTTP(func() {
			GET("/workers")
			Response(StatusOK)
			Response("internal_error", StatusInternalServerError)
		})
	})
})

var _ = Service("credimi", func() {
	Description("Credimi integration endpoints.")

	Error("bad_request", APIError)
	Error("unauthorized", APIError)
	Error("bad_gateway", APIError)
	Error("internal_error", APIError)

	Method("fetch_apk_and_action", func() {
		Payload(func() {
			Attribute("instance_url", String)
			Attribute("version_identifier", String)
			Attribute("action_identifier", String)
			Required("instance_url", "version_identifier")
			Example(map[string]any{
				"instance_url":       "",
				"version_identifier": "",
				"action_identifier":  "",
			})
		})
		Result(FetchApkAndActionResult)

		Error("bad_request", APIError)
		Error("unauthorized", APIError)
		Error("bad_gateway", APIError)
		Error("internal_error", APIError)

		HTTP(func() {
			POST("/credimi/apk-action")
			Response(StatusOK)
			Response("bad_request", StatusBadRequest)
			Response("unauthorized", StatusUnauthorized)
			Response("bad_gateway", StatusBadGateway)
			Response("internal_error", StatusInternalServerError)
		})
	})

	Method("store_pipeline_result", func() {
		Payload(func() {
			Attribute("instance_url", String)
			Attribute("video_path", String)
			Attribute("last_frame_path", String)
			Attribute("logcat_path", String)
			Attribute("run_identifier", String)
			Attribute("runner_identifier", String)
			Required("instance_url", "run_identifier")
			Example(map[string]any{
				"instance_url":      "",
				"video_path":        "",
				"last_frame_path":   "",
				"logcat_path":       "",
				"run_identifier":    "",
				"runner_identifier": "",
			})
		})
		Result(MapOf(String, Any))

		Error("bad_request", APIError)
		Error("unauthorized", APIError)
		Error("bad_gateway", APIError)
		Error("internal_error", APIError)

		HTTP(func() {
			POST("/credimi/pipeline-result")
			Response(StatusOK)
			Response("bad_request", StatusBadRequest)
			Response("unauthorized", StatusUnauthorized)
			Response("bad_gateway", StatusBadGateway)
			Response("internal_error", StatusInternalServerError)
		})
	})
})

var _ = Service("mobile", func() {
	Description("Mobile device control endpoints.")

	Error("internal_error", APIError)

	Method("touch_fingerprint", func() {
		Result(TouchFingerprintResult)

		Error("internal_error", APIError)

		HTTP(func() {
			GET("/mobile/fingerprint/touch")
			Response(StatusOK)
			Response("internal_error", StatusInternalServerError)
		})
	})
})

var _ = Service("health", func() {
	Description("Health check endpoint")

	Error("internal_error", APIError)

	Method("check", func() {
		Result(func() {
			Attribute("status", String)
			Attribute("emulators", ArrayOf(DeviceInfo))
			Required("status")
		})

		Error("internal_error", APIError)

		HTTP(func() {
			GET("/health")
			Response(StatusOK)
			Response("internal_error", StatusInternalServerError)
		})
	})
})

var DeviceInfo = Type("DeviceInfo", func() {
	Attribute("serial", String)
	Attribute("state", String)
	Attribute("product", String)
	Attribute("model", String)
	Attribute("device", String)
	Attribute("transport_id", String)
})
