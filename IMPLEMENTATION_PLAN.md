Here is the Phase 1 product plan for the TalosRed Community Edition.

By utilizing Go and HTMX, you can compile the entire application—proxy router, SQLite database engine, and the web dashboard—into a **single, dependency-free binary**. This perfectly aligns with the "anti-bloat, local-first" positioning that developers crave.

## 1. Architecture & Repository Setup

Because this is managed separately from the enterprise product, it needs to be ruthlessly simple to maintain.

* **Repository Name:** `talosred-ce` (Community Edition) or `talosred-local`.
* **The Single Binary Approach:** Use Go's `go:embed` package to bake the HTML templates, HTMX library, and minimal CSS directly into the compiled executable. There is no `npm install`, no React build step, and no separate frontend service to manage.
* **Local Storage:** Swap PostgreSQL for **SQLite**. The Go binary will automatically create a local `talosred.db` file on startup to store prompt logs and metrics. This eliminates the need for developers to run a database container.
* **Authentication:** Completely open. The server binds only to `127.0.0.1` (localhost) by default, meaning no API keys are required for the dashboard or the proxy endpoint.

## 2. Phase 1 Feature Scope

The goal of Phase 1 is strictly visibility and routing. Do not introduce any enterprise features (blocking, caching, RBAC) into this repository.

| Feature | Implementation Detail | Developer Value |
| --- | --- | --- |
| **Unified Routing** | Expose a standard `/v1/chat/completions` endpoint. Translate OpenAI-formatted requests to Anthropic and Gemini native formats upstream. | Developers only need to write code for one API format, avoiding vendor lock-in. |
| **Cost Calculation** | Hardcode a lightweight JSON file with current per-token costs for major models to calculate exact spend per API call. | Ends "blind" API billing. Shows exactly which features are burning tokens. |
| **Metrics Logging** | Record Time-to-First-Token (TTFT), total latency, input tokens, output tokens, and the raw JSON request/response body into SQLite. | Makes debugging hallucinating or slow LLM calls instantaneous. |
| **OTel Export** | Add a `--otel-endpoint` CLI flag to push traces to external observability stacks (Jaeger, Datadog). | Allows DevOps engineers to integrate the local tool into their wider infrastructure. |

## 3. The HTMX Dashboard Interface

Because you are using HTMX, you can deliver a dynamic, single-page-application feel using only Go standard library `html/template` rendering.

* **The Layout:** A classic two-pane dashboard. Left sidebar for navigation (Metrics, Logs, Settings), main pane for data.
* **Live Request Stream:** Use HTMX Server-Sent Events (SSE) extension (`hx-ext="sse"`). When a developer fires a prompt from their application, the local dashboard updates the log table instantly without a page refresh.
* **The Drill-Down:** Clicking a specific request row uses `hx-get` to fetch the exact prompt, response, and metadata, injecting it into a modal or side-panel.
* **Basic Filtering:** Simple HTML forms utilizing `hx-get` to filter the SQLite database by model (e.g., "Show me all `gpt-4o` calls") or provider.

## 4. The Developer Experience (DX)

The entire pitch rests on getting the developer from "discovery" to "first log" in under 60 seconds.

1. **Download and Run:**
The developer pulls the single Docker image or downloads the binary. They run it locally on port 8080. A `talosred.db` file is instantly initialized.


2. **Configure Environment Variables:**
They provide their upstream keys (e.g., `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`) to the TalosRed binary environment.


3. **Point the Application:**
In their actual application code, they change their LLM SDK's base URL from `api.openai.com` to `http://localhost:8080/v1`.


4. **Instant Visibility:**
They open `http://localhost:8080/ui` in their browser. As they test their app, the dashboard populates in real-time with latency metrics, cost data, and raw prompt logs.


## 5. Phase 1 Go-To-Market

Because this is a developer tool, standard marketing will not work. You need to leverage engineering communities.

* **The README as a Landing Page:** The GitHub README is your primary marketing asset. It must start with a GIF showing the 60-second setup, followed immediately by a latency benchmark proving it is faster than Python-based alternatives.
* **The "Anti-Bloat" Narrative:** When posting to Reddit (`r/golang`, `r/localllama`, `r/machinelearning`) or Hacker News, lean into the architecture. *"We were tired of running heavy Python proxies just to see our LLM logs locally. So we built a single Go binary with an embedded HTMX dashboard."* Engineers love tools that respect their machine's resources.
* **The Upsell Hook:** Inside the local UI, place a single, tasteful button in the sidebar: *"Need Enterprise Blocking & Caching? Try TalosRed Cloud."*