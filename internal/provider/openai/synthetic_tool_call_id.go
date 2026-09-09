package openai

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"reasonix/internal/provider"
)

const syntheticToolCallNamespaceBytes = 12

var syntheticToolCallFallbackSequence atomic.Uint64

// newSyntheticToolCallNamespace returns a short response-scoped namespace for
// tool calls whose OpenAI-compatible stream omitted the provider id.
func newSyntheticToolCallNamespace() string {
	namespace, _ := readSyntheticToolCallNamespace(
		rand.Reader,
		os.Getpid(),
		time.Now().UnixNano(),
		syntheticToolCallFallbackSequence.Add(1),
	)
	return namespace
}

// readSyntheticToolCallNamespace makes the entropy-failure policy explicit and
// testable. Tool-call ids are correlation labels, not secrets, so a crypto/rand
// failure falls back to a bounded digest of process, time, and a process-local
// sequence instead of rejecting an otherwise valid model response.
func readSyntheticToolCallNamespace(random io.Reader, pid int, unixNano int64, sequence uint64) (namespace string, usedFallback bool) {
	var raw [syntheticToolCallNamespaceBytes]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		digest := sha256.Sum256(fmt.Appendf(nil, "%d:%d:%d", pid, unixNano, sequence))
		copy(raw[:], digest[:syntheticToolCallNamespaceBytes])
		usedFallback = true
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), usedFallback
}

func syntheticToolCallID(namespace string, streamIndex int) string {
	return "call_rnx_" + namespace + "_" + strconv.FormatInt(int64(streamIndex), 36)
}

// assignSyntheticToolCallIDs fills only ids omitted by the upstream response.
// One namespace per response plus the streamed call index keeps missing ids
// distinct both within the response and across later turns.
func assignSyntheticToolCallIDs(calls map[int]*provider.ToolCall, order []int) {
	namespace := ""
	for _, index := range order {
		call := calls[index]
		if call == nil || call.ID != "" {
			continue
		}
		if namespace == "" {
			namespace = newSyntheticToolCallNamespace()
		}
		call.ID = syntheticToolCallID(namespace, index)
	}
}
