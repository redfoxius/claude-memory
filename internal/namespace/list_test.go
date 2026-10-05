package namespace

import (
	"reflect"
	"testing"
)

func TestList(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want []Entry
	}{
		{"empty", Config{}, []Entry{
			{"global", []string{}, true},
		}},
		{"only default", Config{Default: "personal"}, []Entry{
			{"personal", []string{}, true},
			{"global", []string{}, false},
		}},
		{"multiple rules, multiple globs, default not among rules", Config{
			Default: "scratch",
			Namespaces: []Rule{
				{Namespace: "pet-game", Paths: []string{"/a", "/a/**"}},
				{Namespace: "work", Paths: []string{"/c/**"}},
				{Namespace: "pet-game", Paths: []string{"/a", "/b"}},
			},
		}, []Entry{
			{"pet-game", []string{"/a", "/a/**", "/b"}, false},
			{"scratch", []string{}, true},
			{"work", []string{"/c/**"}, false},
			{"global", []string{}, false},
		}},
		{"global with rules stays last and keeps paths", Config{
			Default: "global",
			Namespaces: []Rule{
				{Namespace: "global", Paths: []string{"/tmp/**"}},
				{Namespace: "zed", Paths: []string{"/z"}},
			},
		}, []Entry{
			{"zed", []string{"/z"}, false},
			{"global", []string{"/tmp/**"}, true},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.List(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("List() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
