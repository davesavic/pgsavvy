package types

import "testing"

func TestPairNormalKeys(t *testing.T) {
	if PairNormal.Main != QUERY_RAIL {
		t.Errorf("PairNormal.Main = %q, want %q", PairNormal.Main, QUERY_RAIL)
	}
	if PairNormal.Secondary != ResultTabActiveKey {
		t.Errorf("PairNormal.Secondary = %q, want %q", PairNormal.Secondary, ResultTabActiveKey)
	}
}

func TestPairPlanFocusKeys(t *testing.T) {
	if PairPlanFocus.Main != PLAN {
		t.Errorf("PairPlanFocus.Main = %q, want %q", PairPlanFocus.Main, PLAN)
	}
	if PairPlanFocus.Secondary != QUERY_RAIL {
		t.Errorf("PairPlanFocus.Secondary = %q, want %q", PairPlanFocus.Secondary, QUERY_RAIL)
	}
}

func TestResultTabKeyFormat(t *testing.T) {
	cases := []struct {
		i    int
		want ContextKey
	}{
		{0, "result_tab_0"},
		{1, "result_tab_1"},
		{7, "result_tab_7"},
		{12, "result_tab_12"},
	}
	for _, c := range cases {
		if got := ResultTabKey(c.i); got != c.want {
			t.Errorf("ResultTabKey(%d) = %q, want %q", c.i, got, c.want)
		}
	}
}

func TestResultTabActiveKeyConstant(t *testing.T) {
	if ResultTabActiveKey != "result_tab_active" {
		t.Errorf("ResultTabActiveKey = %q, want %q", ResultTabActiveKey, "result_tab_active")
	}
}

func TestMainContextPairZeroValue(t *testing.T) {
	var p MainContextPair
	if p.Main != "" || p.Secondary != "" {
		t.Errorf("zero MainContextPair = %+v, want both fields empty", p)
	}
}
