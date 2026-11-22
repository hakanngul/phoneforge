package dsl

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"pocketlab/internal/device"
)

// exists checks sel once against the live tree.
func (r *runner) exists(ctx context.Context, sel *Selector, action string) (bool, error) {
	if err := checkSelector(sel, action); err != nil {
		return false, err
	}
	_, _, _, err := r.findNode(ctx, sel, action)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, device.ErrElementNotFound) {
		return false, nil
	}
	return false, err
}

// doIfExists runs then (or else) depending on a single predicate check. The
// step itself passes even if an inner step fails; inner on_fail is ignored.
func (r *runner) doIfExists(ctx context.Context, st Step, oc *outcome) error {
	if st.Selector == nil {
		return codeErr(CodeValidation, "if_exists requires predicate")
	}
	ok, err := r.exists(ctx, st.Selector, "if_exists")
	if err != nil {
		return err
	}
	oc.cond = &ok
	if ok {
		oc.thenR, _ = r.execList(ctx, st.Then, false, true)
	} else {
		oc.elseR, _ = r.execList(ctx, st.Else, false, true)
	}
	return nil
}

// doRepeat runs body times / while / until. A failing body step ends the
// loop and fails the repeat step.
func (r *runner) doRepeat(ctx context.Context, st Step, oc *outcome) error {
	modes := 0
	if st.Times != "" {
		modes++
	}
	if st.While != nil {
		modes++
	}
	if st.Until != nil {
		modes++
	}
	if st.Condition != "" {
		modes++
	}
	if modes != 1 {
		return codeErr(CodeValidation, "repeat needs exactly one of times, while, until, condition")
	}
	if len(st.Body) == 0 {
		return codeErr(CodeValidation, "repeat requires a non-empty body")
	}
	if st.Condition != "" {
		return unsupported("repeat", "condition needs a JavaScript runtime; use times, while or until")
	}
	maxIter := st.MaxIterations
	if maxIter <= 0 {
		maxIter = 100
	}
	times := -1
	if st.Times != "" {
		n, err := strconv.Atoi(strings.TrimSpace(string(st.Times)))
		if err != nil || n < 0 {
			return codeErr(CodeValidation, "repeat times %q is not a non-negative integer", st.Times)
		}
		times = n
	}
	iter := 0
	oc.iterations = &iter
	saved := r.vars["repeat_index"]
	defer func() { r.vars["repeat_index"] = saved }()
	for {
		if times >= 0 {
			if iter >= times {
				return nil
			}
		} else {
			if iter >= maxIter {
				if st.Until != nil {
					return codeErr(CodeTimeout, "repeat until: condition not met after %d iterations", maxIter)
				}
				return nil
			}
			sel := st.While
			if st.Until != nil {
				sel = st.Until
			}
			ok, err := r.exists(ctx, sel, "repeat")
			if err != nil {
				return err
			}
			if (st.While != nil && !ok) || (st.Until != nil && ok) {
				return nil
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		r.vars["repeat_index"] = strconv.Itoa(iter)
		res, stop := r.execList(ctx, st.Body, false, false)
		oc.bodyR = append(oc.bodyR, res...)
		iter++
		if stop != "" {
			for _, sr := range res {
				if sr.Status == StatusFailed || sr.Status == StatusNeedsReplan || sr.Status == StatusNeedsUser {
					return &stepError{code: sr.Code, err: errors.New("repeat body step " + strconv.Itoa(sr.Index) + " (" + sr.Action + ") failed in iteration " + strconv.Itoa(iter-1) + ": " + sr.Error)}
				}
			}
			return codeErr(CodeExecutionError, "repeat body stopped")
		}
	}
}
