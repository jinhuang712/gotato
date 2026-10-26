package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// compactReply is what POST /v1/sessions/{id}/compact answers (modelctx.Result).
type compactReply struct {
	Replaced       bool   `json:"replaced"`
	Reason         string `json:"reason"`
	MessagesBefore int    `json:"messages_before"`
	MessagesAfter  int    `json:"messages_after"`
	TokensBefore   int    `json:"tokens_before"`
	TokensAfter    int    `json:"tokens_after"`
	Compaction     *struct {
		Recap            string `json:"recap"`
		Segment          int    `json:"segment"`
		ReplacedMessages int    `json:"replaced_messages"`
	} `json:"compaction"`
}

// podProxy is the API server's proxy to one pod's port 8080: the call goes to that pod, not to any ready one.
func (e *env) podProxy(pod string) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/pods/%s:8080/proxy", e.proxy, e.namespace, pod)
}

// lastInstance is the Instance that claimed the Session last.
func lastInstance(r record) string {
	inst := ""
	for _, ev := range r.Events {
		if ev.Kind == "session_claimed" {
			inst = ev.Instance
		}
	}
	return inst
}

// s9: compaction after a takeover. As s8, a tool is loaded by search and never called, the writer is killed,
// another pod takes the Session over. Then a pod compacts the Session (Gotato's on-demand compaction), and the
// scenario checks the Record, the next message, a read_segment on a pod that did not compact, and a new load.
func (e *env) s9(ctx context.Context) bool {
	fmt.Println("== s9: compaction after a takeover: the loaded tool is unloaded, the Segment is read from another pod")
	const delay = "[quirk:delay=8s]"
	st := newStamper()
	ts := chats("s9", 6)
	tag := func(t *thread, suffix string) string { return fmt.Sprintf("s9-%02d-%s", t.n, suffix) }
	for _, t := range ts {
		m := e.say(ctx, t, tag(t, "a"), "[script:load] "+delay)
		fmt.Printf("%s new thread %d: %s\n", st.at(), t.n, shortDeliveries(m))
	}
	loadedInFlight := func(r record) bool { return inFlight(r) > 0 && r.hasEvent("tool_loaded", catalogTool) }
	victimInst, onVictim, killAt, err := e.killWriter(ctx, st, ts, loadedInFlight, 40*time.Second)
	if err != nil {
		return verdict("s9", false, err.Error())
	}
	onV := map[*thread]bool{}
	sleep(ctx, time.Until(killAt.Add(3*time.Second))) // inside the Liveness TTL, as in s6 and s8
	for _, t := range onVictim {
		onV[t] = true
		m := e.say(ctx, t, tag(t, "b"), "[script:loaded]")
		fmt.Printf("%s loaded? after the kill in %s: %s\n", st.at(), t.session(), shortDeliveries(m))
	}
	e.waitQuiet(ctx, ts, 15*time.Second, 100*time.Second)
	for _, t := range ts {
		if !onV[t] {
			e.say(ctx, t, tag(t, "b"), "[script:loaded]")
		}
	}
	e.waitQuiet(ctx, ts, 6*time.Second, 60*time.Second)

	// The compaction runs on the pod that holds the Session now: the one that took it over, for a victim thread.
	fmt.Printf("%s compacting each Session on the pod that claimed it last\n", st.at())
	faults := 0
	compacted := map[*thread]compactReply{}
	compactor := map[*thread]string{} // the Instance that compacted
	for _, t := range ts {
		r, err := e.record(ctx, t.session())
		if err != nil {
			faults++
			fmt.Printf("  %s %s: cannot read the Record: %v\n", fault, t.session(), err)
			continue
		}
		pod := e.podOf(lastInstance(r))
		if pod == "" {
			faults++
			fmt.Printf("  %s %s: no pod logged instance %.8s\n", fault, t.session(), lastInstance(r))
			continue
		}
		var res compactReply
		if err := e.do(ctx, "POST", e.podProxy(pod)+"/v1/sessions/"+t.session()+"/compact", map[string]int{}, &res); err != nil {
			faults++
			fmt.Printf("  %s %s: compact on %s: %v\n", fault, t.session(), pod, err)
			continue
		}
		compacted[t] = res
		if r2, err := e.record(ctx, t.session()); err == nil {
			if ev, ok := lastCompactionEnded(r2); ok {
				compactor[t] = ev.Instance
			}
		}
	}

	phase := func(suffix, directive string) {
		for _, t := range ts {
			e.say(ctx, t, tag(t, suffix), directive)
		}
		e.waitQuiet(ctx, ts, 4*time.Second, 60*time.Second)
	}
	fmt.Printf("%s next message (loaded?)\n", st.at())
	phase("c", "[script:loaded]")
	// read_segment must run on a pod that did not compact: ask again (up to 4 times) while the pod that
	// answers is the one that compacted. Every ask is a message and is checked like any other.
	fmt.Printf("%s read_segment\n", st.at())
	segTags := map[*thread][]string{}
	for attempt := range 4 {
		suffix := "d"
		if attempt > 0 {
			suffix = fmt.Sprintf("d%d", attempt+1)
		}
		var todo []*thread
		for _, t := range ts {
			if attempt == 0 || !otherPodRead(e, ctx, t, segTags[t], compactor[t]) {
				todo = append(todo, t)
			}
		}
		if len(todo) == 0 {
			break
		}
		for _, t := range todo {
			segTags[t] = append(segTags[t], "T["+tag(t, suffix)+"]")
			e.say(ctx, t, tag(t, suffix), "[script:readseg]")
		}
		e.waitQuiet(ctx, ts, 4*time.Second, 60*time.Second)
	}
	fmt.Printf("%s load again with search\n", st.at())
	phase("e", "[script:search]")

	fmt.Printf("\n%s results\n", st.at())
	var segOtherPod, segSamePod, unverified, unloaded, loadedFalse, reloaded, victimsOK, replacedMsgs int
	for _, t := range ts {
		r, err := e.record(ctx, t.session())
		if err != nil {
			faults++
			fmt.Printf("  %s %s: cannot read the Record: %v\n", fault, t.session(), err)
			continue
		}
		role := "survivor"
		if onV[t] {
			role = "victim"
		}
		res, did := compacted[t]
		tagA := "T[" + tag(t, "a") + "]"
		loads, unloads := loadCounts(r, catalogTool)
		answers := loadedAnswers(r)
		okSeg, badSeg := segmentAnswers(r)
		var found []string
		for _, id := range segTags[t] {
			if v, ok := okSeg[id]; ok {
				found = append(found, fmt.Sprintf("%s ok on %s", id, ifElse(instanceOfTag(r, id) == compactor[t], "the compacting pod", "another pod")))
				if instanceOfTag(r, id) != compactor[t] && strings.Contains(v, tagA) {
					segOtherPod++
				} else {
					segSamePod++
				}
			} else if _, bad := badSeg[id]; bad {
				found = append(found, id+" failed")
			}
		}
		compactionLine := "not compacted"
		if did {
			compactionLine = fmt.Sprintf("replaced=%v %s messages %d -> %d, tokens %d -> %d", res.Replaced, res.Reason, res.MessagesBefore, res.MessagesAfter, res.TokensBefore, res.TokensAfter)
			if res.Compaction != nil {
				replacedMsgs += res.Compaction.ReplacedMessages
			}
		}
		fmt.Printf("  %s (%s): %s\n", t.session(), role, compactionLine)
		fmt.Printf("    Segments %d, Recap writer %q, Recap text %d bytes, Segment 1 holds %s: %v; tool_unloaded(compaction) %v; tool_loaded x%d, tool_unloaded x%d; tools now %v\n",
			len(r.Segments), lastRecapWriter(r), len(segmentRecap(r, 1)), tagA, segmentHolds(r, 1, tagA), unloadedByCompaction(r), loads, unloads, r.Tools)
		fmt.Printf("    loaded? answers %v; read_segment: %s; Runs %s\n", answers, strings.Join(found, ", "), runStatuses(r))
		if onV[t] {
			printTakeover(t, r, victimInst, killAt)
		}
		if !did || !res.Replaced {
			faults++
			fmt.Printf("  %s %s: the compaction replaced nothing\n", fault, t.session())
			continue
		}
		if len(r.Segments) != 1 || len(r.Compactions) != 1 || lastRecapWriter(r) != "truncate" || segmentRecap(r, 1) == "" {
			faults++
			fmt.Printf("  %s %s: want 1 Segment with a truncate Recap: segments %d, compactions %d, writer %q\n", fault, t.session(), len(r.Segments), len(r.Compactions), lastRecapWriter(r))
		}
		if !segmentHolds(r, 1, tagA) {
			faults++
			fmt.Printf("  %s %s: Segment 1 does not hold the first message %s word for word\n", fault, t.session(), tagA)
		}
		if got := unloadedByCompaction(r); !slices.Equal(got, []string{catalogTool}) {
			faults++
			fmt.Printf("  %s %s: tool_unloaded with why=compaction is %v, want [%s]\n", fault, t.session(), got, catalogTool)
		} else {
			unloaded++
		}
		if len(answers) == 2 && answers[0] && !answers[1] {
			loadedFalse++
			if onV[t] {
				victimsOK++
			}
		} else {
			faults++
			fmt.Printf("  %s %s: loaded? answers %v, want [true false] (loaded before the compaction, not after)\n", fault, t.session(), answers)
		}
		if a := checkMCP(r); a.OK["T["+tag(t, "e")+"]"] == 1 && loads == 2 && unloads == 1 && slices.Equal(r.Tools, []string{catalogTool}) {
			reloaded++
		} else {
			faults++
			fmt.Printf("  %s %s: loading again: mcp-ok %d (want 1), tool_loaded x%d (want 2), tool_unloaded x%d (want 1), tools %v\n", fault, t.session(), a.OK["T["+tag(t, "e")+"]"], loads, unloads, r.Tools)
		}
		if len(badSeg) > 0 {
			faults++
			fmt.Printf("  %s %s: read_segment failed: %v\n", fault, t.session(), badSeg)
		}
		if !otherPodRead(e, ctx, t, segTags[t], compactor[t]) {
			unverified++
			fmt.Printf("  not verified: %s never had read_segment answered by a pod other than the compacting one\n", t.session())
		}
	}
	fmt.Println()
	faults += e.report(ctx, ts)
	for _, s := range storeErrors(e.readLogs()) {
		faults++
		fmt.Printf("  %s store error in a bot log: %s\n", fault, s)
	}
	fmt.Printf("  threads %d (the killed pod wrote %d); compactions that replaced messages %d (%d Messages closed in all)\n", len(ts), len(onVictim), len(compacted), replacedMsgs)
	fmt.Printf("  tool_unloaded (why=compaction) for %s: %d of %d; next message saw the tool not loaded: %d of %d (victims %d of %d); loaded again by search: %d of %d\n",
		catalogTool, unloaded, len(ts), loadedFalse, len(ts), victimsOK, len(onVictim), reloaded, len(ts))
	fmt.Printf("  read_segment results with the first message in them: from another pod %d, from the compacting pod %d; threads where no other pod answered %d\n", segOtherPod, segSamePod, unverified)
	return verdict("s9", faults == 0, fmt.Sprintf("%d GOTATO-FAULT", faults))
}

// otherPodRead says one of the read_segment asks of the thread was answered ok by an Instance other than the
// one that compacted.
func otherPodRead(e *env, ctx context.Context, t *thread, tags []string, compactor string) bool {
	if len(tags) == 0 {
		return false
	}
	r, err := e.record(ctx, t.session())
	if err != nil {
		return false
	}
	okSeg, _ := segmentAnswers(r)
	for _, id := range tags {
		if _, ok := okSeg[id]; ok && instanceOfTag(r, id) != compactor {
			return true
		}
	}
	return false
}

func lastRecapWriter(r record) string {
	if len(r.Compactions) == 0 {
		return ""
	}
	return r.Compactions[len(r.Compactions)-1].Recap
}

func ifElse(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}
