package backend

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestEncodeID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
		want struct {
			result string
			err    error
		}
	}{
		{
			name: "base64 without slash",
			id:   "mFrrgfLpmiSLw6bjO9ZS7F1d7I5fb2DQO3Br5W5e3U0=",
			want: struct {
				result string
				err    error
			}{
				result: "mFrrgfLpmiSLw6bjO9ZS7F1d7I5fb2DQO3Br5W5e3U0=",
			},
		},
		{
			name: "base64 with one slash",
			id:   "eqWF/jnj8u+hl4RcMhv+53OR",
			want: struct {
				result string
				err    error
			}{
				result: "eqWF-jnj8u+hl4RcMhv+53OR",
			},
		},
		{
			name: "base64 with multiple slashes",
			id:   "eq/WF/jn/j8u+hl4RcMhv+53OR",
			want: struct {
				result string
				err    error
			}{
				result: "eq-WF-jn-j8u+hl4RcMhv+53OR",
			},
		},
		{
			name: "base64 with padding",
			id:   "YWJjZA==",
			want: struct {
				result string
				err    error
			}{
				result: "YWJjZA==",
			},
		},
		{
			name: "empty string",
			id:   "",
			want: struct {
				result string
				err    error
			}{
				result: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := encodeID(tt.id)
			if diff := cmp.Diff(tt.want.result, got); diff != "" {
				t.Errorf("encodeID result mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
