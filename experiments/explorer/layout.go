package main

import (
	"math"
	"math/rand"
)

// layout places nodes in 3D with a force simulation that follows d3-force:
// links pull with strength 1/min(degree), nodes repel through a Barnes-Hut
// octree, and the centre of mass is kept at the origin. It runs at build time
// so the browser only renders.
func layout(nodes []*Node, links []Link, iterations int) {
	n := len(nodes)
	if n == 0 {
		return
	}
	pos := make([][3]float64, n)
	vel := make([][3]float64, n)
	charge := make([]float64, n)
	rng := rand.New(rand.NewSource(1))
	r0 := 10 * math.Cbrt(float64(n))
	for i := range pos {
		// Uniform in a ball.
		for {
			p := [3]float64{rng.Float64()*2 - 1, rng.Float64()*2 - 1, rng.Float64()*2 - 1}
			if p[0]*p[0]+p[1]*p[1]+p[2]*p[2] <= 1 {
				pos[i] = [3]float64{p[0] * r0, p[1] * r0, p[2] * r0}
				break
			}
		}
	}
	count := make([]int, n)
	for _, l := range links {
		count[l.S]++
		count[l.T]++
	}
	for i := range charge {
		charge[i] = -45 - 6*math.Sqrt(float64(count[i]))
	}
	dist := map[string]float64{"contains": 18, "touches": 40, "flow": 110, "stage": 90, "label": 70, "topic": 60,
		"authored": 120, "committed": 120, "engaged": 130, "assigned": 120, "scope": 55}

	hub := max(200, n/12)
	alpha, alphaMin := 1.0, 0.001
	alphaDecay := 1 - math.Pow(alphaMin, 1/float64(iterations))
	const velocityDecay = 0.4
	const theta2 = 0.81
	tree := &octree{}
	for it := 0; it < iterations; it++ {
		// Links.
		for _, l := range links {
			s, t := l.S, l.T
			d := 45.0
			if v, ok := dist[l.Kind]; ok {
				d = v
			}
			strength := 1 / float64(min(count[s], count[t]))
			// Super-hubs (the main author, "opened", "closed") link to most
			// items; at full strength they pull every item into one ball.
			if max(count[s], count[t]) > hub {
				strength *= 0.08
			}
			bias := float64(count[s]) / float64(count[s]+count[t])
			var x [3]float64
			ll := 0.0
			for k := 0; k < 3; k++ {
				x[k] = pos[t][k] + vel[t][k] - pos[s][k] - vel[s][k]
				ll += x[k] * x[k]
			}
			ll = math.Sqrt(ll)
			if ll < 1e-6 {
				ll = 1e-6
			}
			f := (ll - d) / ll * alpha * strength
			for k := 0; k < 3; k++ {
				vel[t][k] -= x[k] * f * bias
				vel[s][k] += x[k] * f * (1 - bias)
			}
		}
		// Many-body.
		tree.build(pos, charge)
		for i := range pos {
			tree.apply(0, i, pos, vel, alpha, theta2)
		}
		// Integrate and centre.
		var c [3]float64
		for i := range pos {
			for k := 0; k < 3; k++ {
				vel[i][k] *= 1 - velocityDecay
				pos[i][k] += vel[i][k]
				c[k] += pos[i][k]
			}
		}
		for i := range pos {
			for k := 0; k < 3; k++ {
				pos[i][k] -= c[k] / float64(n)
			}
		}
		alpha += (0 - alpha) * alphaDecay
		if alpha < alphaMin {
			break
		}
	}
	for i, nd := range nodes {
		for k := 0; k < 3; k++ {
			nd.P[k] = math.Round(pos[i][k]*10) / 10
		}
	}
}

type octCell struct {
	center   [3]float64 // cell centre
	half     float64
	mass     float64 // summed charge
	com      [3]float64
	children [8]int32 // index into cells, 0 = none (cell 0 is the root)
	point    int32    // single point index, -1 when internal or empty
	leaf     bool
}

type octree struct{ cells []octCell }

func (t *octree) build(pos [][3]float64, charge []float64) {
	lo, hi := pos[0], pos[0]
	for _, p := range pos {
		for k := 0; k < 3; k++ {
			lo[k] = math.Min(lo[k], p[k])
			hi[k] = math.Max(hi[k], p[k])
		}
	}
	half := 0.0
	var c [3]float64
	for k := 0; k < 3; k++ {
		half = math.Max(half, (hi[k]-lo[k])/2)
		c[k] = (hi[k] + lo[k]) / 2
	}
	t.cells = t.cells[:0]
	t.cells = append(t.cells, octCell{center: c, half: half + 1, point: -1, leaf: true})
	for i := range pos {
		t.insert(0, int32(i), pos, 0)
	}
	t.summarize(0, pos, charge)
}

func (t *octree) octant(cell int, p [3]float64) int {
	o := 0
	for k := 0; k < 3; k++ {
		if p[k] >= t.cells[cell].center[k] {
			o |= 1 << k
		}
	}
	return o
}

func (t *octree) child(cell, o int) int {
	if ch := t.cells[cell].children[o]; ch != 0 {
		return int(ch)
	}
	h := t.cells[cell].half / 2
	c := t.cells[cell].center
	for k := 0; k < 3; k++ {
		if o&(1<<k) != 0 {
			c[k] += h
		} else {
			c[k] -= h
		}
	}
	t.cells = append(t.cells, octCell{center: c, half: h, point: -1, leaf: true})
	idx := len(t.cells) - 1
	t.cells[cell].children[o] = int32(idx)
	return idx
}

func (t *octree) insert(cell int, i int32, pos [][3]float64, depth int) {
	for {
		c := &t.cells[cell]
		if c.leaf && c.point < 0 {
			c.point = i
			return
		}
		if c.leaf {
			// Coincident points or too deep: keep a chain in one leaf by
			// nudging; deep splits on identical points never terminate.
			if depth > 40 {
				pos[i][0] += 1e-3
				return
			}
			old := c.point
			c.point, c.leaf = -1, false
			t.insert(t.child(cell, t.octant(cell, pos[old])), old, pos, depth+1)
		}
		cell = t.child(cell, t.octant(cell, pos[i]))
		depth++
	}
}

func (t *octree) summarize(cell int, pos [][3]float64, charge []float64) {
	c := &t.cells[cell]
	if c.leaf {
		if c.point >= 0 {
			c.mass = charge[c.point]
			c.com = pos[c.point]
		}
		return
	}
	var m float64
	var com [3]float64
	for _, ch := range c.children {
		if ch == 0 {
			continue
		}
		t.summarize(int(ch), pos, charge)
		cc := &t.cells[ch]
		m += cc.mass
		for k := 0; k < 3; k++ {
			com[k] += cc.com[k] * cc.mass
		}
	}
	c = &t.cells[cell]
	c.mass = m
	if m != 0 {
		for k := 0; k < 3; k++ {
			c.com[k] = com[k] / m
		}
	}
}

func (t *octree) apply(cell, i int, pos, vel [][3]float64, alpha, theta2 float64) {
	c := &t.cells[cell]
	if c.mass == 0 || (c.leaf && int(c.point) == i) {
		return
	}
	var x [3]float64
	l := 0.0
	for k := 0; k < 3; k++ {
		x[k] = c.com[k] - pos[i][k]
		l += x[k] * x[k]
	}
	w := 2 * c.half
	if c.leaf || w*w/theta2 < l {
		if l < 1 {
			l = 1
		}
		f := c.mass * alpha / l
		for k := 0; k < 3; k++ {
			vel[i][k] += x[k] * f
		}
		return
	}
	for _, ch := range c.children {
		if ch != 0 {
			t.apply(int(ch), i, pos, vel, alpha, theta2)
		}
	}
}
