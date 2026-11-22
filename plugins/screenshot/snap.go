package main

import "image"

// snapTarget is a window recorded together with the frozen desktop, in
// physical desktop pixels. Siblings are ordered front to back, like the
// screen they describe; children are already clipped to their parent.
type snapTarget struct {
	bounds   image.Rectangle
	children []snapTarget
}

// snapAt returns the front-most window under p, narrowed to its deepest
// child. An empty rectangle means no recorded window contains the point.
func snapAt(targets []snapTarget, p image.Point) image.Rectangle {
	for _, t := range targets {
		if p.In(t.bounds) {
			if r := snapAt(t.children, p); !r.Empty() {
				return r
			}
			return t.bounds
		}
	}
	return image.Rectangle{}
}
