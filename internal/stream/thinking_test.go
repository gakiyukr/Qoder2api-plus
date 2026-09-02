package stream

import "testing"

func TestThinkingTagsAcrossChunksAndChannels(t *testing.T) {
	p := &ThinkingRouter{}
	var reasoning, content string
	feeds := []struct {
		s string
		r bool
	}{{"<thi", true}, {"nking>plan", true}, {" more</thin", false}, {"king>answer", false}}
	for _, f := range feeds {
		for _, x := range p.Feed(f.s, f.r) {
			if x.Reasoning {
				reasoning += x.Text
			} else {
				content += x.Text
			}
		}
	}
	for _, x := range p.Finalize() {
		if x.Reasoning {
			reasoning += x.Text
		} else {
			content += x.Text
		}
	}
	if reasoning != "plan more" || content != "answer" {
		t.Fatalf("reasoning=%q content=%q", reasoning, content)
	}
}

func TestMultipleTagVariants(t *testing.T) {
	p := &ThinkingRouter{}
	var r, c string
	for _, x := range p.Feed("a<think>b</think>c<reasoning>d</reasoning>e", false) {
		if x.Reasoning {
			r += x.Text
		} else {
			c += x.Text
		}
	}
	if r != "bd" || c != "ace" {
		t.Fatalf("r=%q c=%q", r, c)
	}
}
