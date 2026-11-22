package main

import "image"

// snapTarget is a window or a part of one, recorded for the frozen desktop
// in physical desktop pixels. Top-level windows and child windows are ordered
// front to back, like the screen they describe, and children are clipped to
// their parent.
type snapTarget struct {
	bounds   image.Rectangle
	children []snapTarget
	// loose marks children that are interface elements rather than windows:
	// their order says nothing about what is in front, so the smallest wins.
	loose bool
	hwnd  uintptr // top-level windows only
}

// snapChain lists what a point can snap to, from the front-most window under
// it down to the smallest part, without repeating equal rectangles.
func snapChain(targets []snapTarget, p image.Point) []image.Rectangle {
	for _, t := range targets {
		if p.In(t.bounds) {
			return t.chain(nil, p)
		}
	}
	return nil
}

func (t snapTarget) chain(chain []image.Rectangle, p image.Point) []image.Rectangle {
	if len(chain) == 0 || chain[len(chain)-1] != t.bounds {
		chain = append(chain, t.bounds)
	}
	var best []image.Rectangle
	for _, child := range t.children {
		if !p.In(child.bounds) {
			continue
		}
		if !t.loose {
			return child.chain(chain, p)
		}
		// Each candidate extends its own copy of the path so far.
		candidate := child.chain(append([]image.Rectangle(nil), chain...), p)
		if a, b := candidate[len(candidate)-1], best; b == nil || a.Dx()*a.Dy() < b[len(b)-1].Dx()*b[len(b)-1].Dy() {
			best = candidate
		}
	}
	if best != nil {
		return best
	}
	return chain
}

// snapAt is the smallest part under p; empty when no recorded window contains it.
func snapAt(targets []snapTarget, p image.Point) image.Rectangle {
	if chain := snapChain(targets, p); len(chain) > 0 {
		return chain[len(chain)-1]
	}
	return image.Rectangle{}
}

// snapOrder picks the windows worth a closer look: the one under the pointer
// first, then front to back, skipping any that an earlier window hides completely.
func snapOrder(targets []snapTarget, pointer image.Point, limit int) []int {
	var order []int
	first := -1
	for i, t := range targets {
		hidden := false
		for _, front := range targets[:i] {
			if t.bounds.In(front.bounds) {
				hidden = true
				break
			}
		}
		if hidden {
			continue
		}
		if first < 0 && pointer.In(t.bounds) {
			first = i
			order = append([]int{i}, order...)
			continue
		}
		order = append(order, i)
	}
	if len(order) > limit {
		order = order[:limit]
	}
	return order
}
