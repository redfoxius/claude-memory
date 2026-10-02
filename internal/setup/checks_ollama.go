package setup

import (
	"context"
	"fmt"
	"net"
	"net/url"
)

// Doctor checks ollama.reachable, ollama.model, ollama.embed (AC-32, AC-58).
// GET /api/version, GET /api/tags and one POST /api/embed; never a pull.

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func (d *doctor) checkOllamaReachable(ctx context.Context) (Status, string, string) {
	u := d.ollamaURL()
	v, err := d.Ollama.Version(ctx, u)
	if err != nil {
		remedy := "start Ollama (macOS: `brew services start ollama`; Linux: `systemctl start ollama` or `ollama serve`), or set MEMORY_OLLAMA_URL"
		if !isLoopbackURL(u) {
			remedy = "check MEMORY_OLLAMA_URL and that the remote Ollama is reachable from here"
		}
		return StatusFail, "no Ollama at " + u + ": " + d.redact(err.Error()), remedy
	}
	detail := fmt.Sprintf("Ollama %s at %s", v, u)
	if !isLoopbackURL(u) {
		detail += " (remote: the prompt hook's 800 ms budget includes the network round trip)"
	}
	return pass(detail)
}

func (d *doctor) checkOllamaModel(ctx context.Context) (Status, string, string) {
	u, m := d.ollamaURL(), d.ollamaModel()
	ok, err := d.Ollama.HasModel(ctx, u, m)
	switch {
	case err != nil:
		return StatusFail, "cannot list models: " + d.redact(err.Error()), "check the Ollama server logs"
	case !ok:
		return StatusFail, "model " + m + " is not pulled", "ollama pull " + m
	}
	return pass("model " + m + " present")
}

func (d *doctor) checkOllamaEmbed(ctx context.Context) (Status, string, string) {
	u, m := d.ollamaURL(), d.ollamaModel()
	dims, lat, err := d.Ollama.EmbedDims(ctx, u, m)
	switch {
	case err != nil:
		return StatusFail, "embedding failed: " + d.redact(err.Error()), "check the Ollama server logs; `ollama run " + m + "` reports load errors"
	case dims != SchemaEmbeddingDims:
		return StatusFail, fmt.Sprintf("model %s returns %d dims, schema needs %d", m, dims, SchemaEmbeddingDims),
			"set MEMORY_OLLAMA_MODEL to a 1024-dimension model (bge-m3)"
	case lat > EmbedLatencyWarn:
		return StatusWarn, fmt.Sprintf("%d dims, but one embed took %s (> %s): the prompt hook gives up after MEMORY_HOOK_TIMEOUT (800 ms by default) and then injects nothing",
				dims, roundDur(lat), roundDur(EmbedLatencyWarn)),
			"re-run doctor (the first call loads the model); a remote CPU Ollama is too slow for the hook — use a local one (integration/ollama.md)"
	}
	return pass(fmt.Sprintf("%d dims in %s", dims, roundDur(lat)))
}
