package main

// Tool catalog exposed to MCP clients.
//
// Keep this list deliberately small and explicit. The REST API has more
// routes (see cmd/arbox/http_handlers.go), but LLM context windows care
// about tool count, and every tool added here is another thing the model
// has to remember not to call when it isn't useful. Add a new entry only
// when there's a clear user workflow for the LLM to drive.
//
// Schema note: arguments follow JSON Schema draft-07 subset. MCP clients
// use these to validate model output before dispatching to us, so keep
// the shapes honest. `additionalProperties: false` is omitted on
// permissive tools (the upstream handler validates) but set on mutations
// to reduce surprise.

type toolDef struct {
	name   string
	method string // "GET" or "POST"
	path   string // upstream REST path, e.g. "/api/v1/status"
	desc   string
	schema map[string]any // JSON Schema for `arguments`
}

var toolDefs = []toolDef{
	{
		name:   "arbox_version",
		method: "GET",
		path:   "/api/v1/version",
		desc:   "Build rev, gym, timezone, lookahead, pause snapshot. Cheap; use first to confirm the daemon is reachable and which version is running.",
		schema: emptyObjectSchema(),
	},
	{
		name:   "arbox_status",
		method: "GET",
		path:   "/api/v1/status",
		desc:   "Saved weekly plan per weekday + the user's actual Arbox bookings (BOOKED / WAITLIST with position). Preferred for 'what am I booked into and what is the plan for the week'.",
		schema: map[string]any{
			"type":        "object",
			"description": "Optional lookahead window.",
			"properties": map[string]any{
				"days": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     30,
					"default":     7,
					"description": "Number of calendar days to summarize (default 7).",
				},
			},
		},
	},
	{
		name:   "arbox_classes",
		method: "GET",
		path:   "/api/v1/classes",
		desc:   "Live class list for one calendar date with capacity + the user's registration status (BOOKED / WAITLIST / '-').",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"date": map[string]any{
					"type":        "string",
					"pattern":     `^\d{4}-\d{2}-\d{2}$`,
					"description": "Date in YYYY-MM-DD (gym timezone).",
				},
				"filter": map[string]any{
					"type":        "string",
					"description": "Optional case-insensitive substring filter on the class category (e.g. 'Hall A', 'Weightlifting').",
				},
			},
			"required":             []string{"date"},
			"additionalProperties": false,
		},
	},
	{
		name:   "arbox_morning",
		method: "GET",
		path:   "/api/v1/morning",
		desc:   "Compact morning (06-12 by default) listing across the next N days with BOOKED/WAITLIST tags.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"from": map[string]any{"type": "integer", "minimum": 0, "maximum": 23, "default": 6},
				"to":   map[string]any{"type": "integer", "minimum": 1, "maximum": 24, "default": 12},
				"days": map[string]any{"type": "integer", "minimum": 1, "maximum": 30, "default": 1},
			},
		},
	},
	{
		name:   "arbox_evening",
		method: "GET",
		path:   "/api/v1/evening",
		desc:   "Compact evening (16-22 by default) listing across the next N days with BOOKED/WAITLIST tags.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"from": map[string]any{"type": "integer", "minimum": 0, "maximum": 23, "default": 16},
				"to":   map[string]any{"type": "integer", "minimum": 1, "maximum": 24, "default": 22},
				"days": map[string]any{"type": "integer", "minimum": 1, "maximum": 30, "default": 1},
			},
		},
	},
	{
		name:   "arbox_bookings",
		method: "GET",
		path:   "/api/v1/bookings",
		desc:   "Explicit list of currently BOOKED + WAITLIST classes with waitlist position when known. Use this when the user asks 'am I on any waitlist right now'.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"days": map[string]any{"type": "integer", "minimum": 1, "maximum": 30, "default": 14},
			},
		},
	},
	{
		name:   "arbox_plan",
		method: "GET",
		path:   "/api/v1/plan",
		desc:   "Merged weekly plan (config.yaml + user_plan.yaml overlay) that the daemon actually uses. Read this before asking the user to change the plan.",
		schema: emptyObjectSchema(),
	},
	{
		name:   "arbox_selftest",
		method: "GET",
		path:   "/api/v1/selftest",
		desc:   "Run the daemon's 8 health checks (config, auth, Arbox reachability, gym discovery, membership, schedule, attempts file, plan resolves). Call this if something seems wrong.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"days": map[string]any{"type": "integer", "minimum": 1, "maximum": 30, "default": 7},
			},
		},
	},

	// ---- Mutations below. All require the admin token AND confirm:true. ----

	{
		name:   "arbox_book",
		method: "POST",
		path:   "/api/v1/book",
		desc:   "Book one class by schedule_id. Without confirm=true, returns a dry-run preview and writes nothing to Arbox. With confirm=true, really books (idempotent via booking_attempts.json). Always preview first and ask the user to confirm.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"schedule_id": map[string]any{
					"type":        "integer",
					"description": "Arbox schedule_id of the target class (see arbox_classes output).",
				},
				"confirm": map[string]any{
					"type":        "boolean",
					"default":     false,
					"description": "Set true only after the user explicitly confirmed the dry-run preview.",
				},
			},
			"required":             []string{"schedule_id"},
			"additionalProperties": false,
		},
	},
	{
		name:   "arbox_cancel",
		method: "POST",
		path:   "/api/v1/cancel",
		desc:   "Cancel an existing BOOKED class. Dry-run unless confirm=true.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"schedule_id": map[string]any{"type": "integer"},
				"confirm":     map[string]any{"type": "boolean", "default": false},
			},
			"required":             []string{"schedule_id"},
			"additionalProperties": false,
		},
	},
	{
		name:   "arbox_pause",
		method: "POST",
		path:   "/api/v1/pause",
		desc:   "Pause auto-booking for a duration (e.g. '3d', '6h', 'until 2026-04-25'). Dry-run unless confirm=true.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"duration": map[string]any{
					"type":        "string",
					"description": "Duration ('3d', '6h') OR 'until YYYY-MM-DD [HH:MM]'.",
				},
				"reason":  map[string]any{"type": "string"},
				"confirm": map[string]any{"type": "boolean", "default": false},
			},
			"required":             []string{"duration"},
			"additionalProperties": false,
		},
	},
	{
		name:   "arbox_resume",
		method: "POST",
		path:   "/api/v1/resume",
		desc:   "Clear any active pause. Dry-run unless confirm=true.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"confirm": map[string]any{"type": "boolean", "default": false},
			},
			"additionalProperties": false,
		},
	},

	// ---- Escape hatch: passthrough to upstream Arbox member API. ----

	{
		name:   "arbox_api_query",
		method: "POST",
		path:   "/api/v1/arbox/query",
		desc: "Call any Arbox member API endpoint (https://apiappv2.arboxapp.com/api/v2/...) " +
			"through the daemon, reusing the authenticated session + Cloudflare-passing " +
			"headers. Use this when the specific arbox_* tools above don't cover what " +
			"the user asked for — e.g. reading the user feed, listing memberships, pulling " +
			"notifications, checking box metadata. " +
			"Path must start with /api/v2/. Auth endpoints (login/logout/refresh/password) " +
			"are blocked. The response includes { status, json | body_base64, body_is_json } " +
			"so you can inspect non-2xx upstream errors yourself. Requires the admin token.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"method": map[string]any{
					"type":        "string",
					"enum":        []string{"GET", "POST", "PUT", "PATCH", "DELETE"},
					"default":     "GET",
					"description": "HTTP method to use against the upstream Arbox API.",
				},
				"path": map[string]any{
					"type":        "string",
					"pattern":     `^/api/v2/[A-Za-z0-9/_\-.?&=%]+$`,
					"description": "Upstream path including leading slash, e.g. '/api/v2/user/feed' or '/api/v2/boxes/1130/memberships/1'.",
				},
				"body": map[string]any{
					"type":        "object",
					"description": "JSON body for POST/PUT/PATCH. Omit for GET. Forwarded verbatim.",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
	},
}

// toolIndex is the O(1) lookup used by tools/call.
var toolIndex = func() map[string]toolDef {
	m := make(map[string]toolDef, len(toolDefs))
	for _, d := range toolDefs {
		m[d.name] = d
	}
	return m
}()

// mcpTools renders the MCP tools/list shape from toolDefs.
func mcpTools() []map[string]any {
	out := make([]map[string]any, 0, len(toolDefs))
	for _, d := range toolDefs {
		out = append(out, map[string]any{
			"name":        d.name,
			"description": d.desc,
			"inputSchema": d.schema,
		})
	}
	return out
}

func emptyObjectSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}
