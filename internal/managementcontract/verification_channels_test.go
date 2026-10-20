package managementcontract

import (
	"reflect"
	"testing"
)

func TestVerificationChannelsCanonicalAndInvalid(t *testing.T) {
	for _, tc := range []struct{ input, want []Channel }{
		{nil, []Channel{ChannelBuild}},
		{[]Channel{ChannelPlayground}, []Channel{ChannelPlayground}},
		{[]Channel{ChannelPlayground, ChannelBuild}, []Channel{ChannelBuild, ChannelPlayground}},
	} {
		got, err := NormalizeVerificationChannels(tc.input)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%v -> %v, %v", tc.input, got, err)
		}
	}
	for _, bad := range [][]Channel{{}, {"other"}, {ChannelBuild, ChannelBuild}, {ChannelBuild, ChannelPlayground, ChannelBuild}} {
		if _, err := NormalizeVerificationChannels(bad); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}
