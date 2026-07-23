"""Generate Syncdesic logo: deltoidal icositetrahedron wireframe
over Syncthing-style blue gradient circle.

Vertex coordinates from Polytope Wiki (dual edge length = 1):
  Red (4-fold): (±√2, 0, 0) permutations — 6 vertices
  Blue (2-fold): (±1, ±1, 0) permutations — 12 vertices
  Yellow (3-fold): (±b, ±b, ±b), b = (√2+4)/7 — 8 vertices
  Faces: each kite = 1 Red + 2 Blues + 1 Yellow

Design: 13 independent active edges (vertex degree ≤ 2, geodesic unitary
memory constraint), each rendered as a 3-layer glow data packet with random
progress length, comet head at leading edge, and node dots at both endpoints.
Rest are dashed background lines.
"""

import math
import itertools
import random

# ---------------------------------------------------------------------------
# 1. Geometry
# ---------------------------------------------------------------------------
SQRT2 = math.sqrt(2)
B = (SQRT2 + 4) / 7


def _reds():
    pts = set()
    for perm in itertools.permutations([SQRT2, 0, 0]):
        for sx in (-1, 1):
            for sy in (-1, 1):
                for sz in (-1, 1):
                    v = (perm[0] * sx, perm[1] * sy, perm[2] * sz)
                    non_zero = sum(1 for c in v if abs(c) > 1e-9)
                    if non_zero == 1:
                        pts.add(tuple(round(c, 12) for c in v))
    return list(pts)


def _blues():
    pts = set()
    for perm in itertools.permutations([1, 1, 0]):
        for sx in (-1, 1):
            for sy in (-1, 1):
                for sz in (-1, 1):
                    v = (perm[0] * sx, perm[1] * sy, perm[2] * sz)
                    non_zero = sum(1 for c in v if abs(c) > 1e-9)
                    if non_zero == 2:
                        pts.add(tuple(round(c, 12) for c in v))
    return list(pts)


def _yellows():
    pts = []
    for sx in (-1, 1):
        for sy in (-1, 1):
            for sz in (-1, 1):
                pts.append((B * sx, B * sy, B * sz))
    return pts


# ---------------------------------------------------------------------------
# 2. Edge detection
# ---------------------------------------------------------------------------
LONG_SQ = sum((a - b) ** 2 for a, b in zip((SQRT2, 0, 0), (1, 1, 0)))
SHORT_SQ = sum((a - b) ** 2 for a, b in zip((1, 1, 0), (B, B, B)))
TOL = 1e-6


def is_edge(v1, v2, target_sq):
    d2 = sum((a - b) ** 2 for a, b in zip(v1, v2))
    return abs(d2 - target_sq) < TOL


# ---------------------------------------------------------------------------
# 3. Build graph
# ---------------------------------------------------------------------------
verts = list(_reds()) + _blues() + _yellows()
types = []
for v in verts:
    nz = sum(1 for c in v if abs(c) > 1e-9)
    if nz == 1:
        types.append('R')
    elif nz == 2:
        types.append('B')
    else:
        types.append('Y')

adj = {i: set() for i in range(len(verts))}
for ri in [i for i, t in enumerate(types) if t == 'R']:
    for bj in [j for j, t in enumerate(types) if t == 'B']:
        if is_edge(verts[ri], verts[bj], LONG_SQ):
            adj[ri].add(bj)
            adj[bj].add(ri)
for bj in [j for j, t in enumerate(types) if t == 'B']:
    for yk in [k for k, t in enumerate(types) if t == 'Y']:
        if is_edge(verts[bj], verts[yk], SHORT_SQ):
            adj[bj].add(yk)
            adj[yk].add(bj)

edge_list = []
for i in range(len(verts)):
    for j in adj[i]:
        if i < j:
            edge_list.append((i, j))

# Verify faces
faces = []
for ri in [i for i, t in enumerate(types) if t == 'R']:
    rn = list(adj[ri])
    for i in range(len(rn)):
        for j in range(i + 1, len(rn)):
            common = adj[rn[i]] & adj[rn[j]] & {k for k, t in enumerate(types) if t == 'Y'}
            for yk in common:
                faces.append([ri, rn[i], yk, rn[j]])
unique_faces = set()
for f in faces:
    mp = min(range(4), key=lambda p: f[p])
    unique_faces.add(tuple(f[mp:] + f[:mp]))
faces = list(unique_faces)
print(f"Vertices: {len(verts)}  Faces: {len(faces)}  Edges: {len(edge_list)}")

# ---------------------------------------------------------------------------
# 4. Active edges — degree-2 constraint (geodesic unitary memory)
# Each vertex may have at most 2 incident active edges (one in, one out).
# Greedy selection from shuffled edge list.
# ---------------------------------------------------------------------------
shuffled = list(edge_list)
random.shuffle(shuffled)

degree = {i: 0 for i in range(len(verts))}
active_set = []
for i, j in shuffled:
    if len(active_set) >= 13:
        break
    if degree[i] < 2 and degree[j] < 2:
        active_set.append((i, j))
        degree[i] += 1
        degree[j] += 1

# Assign direction for each active edge (which end is the source/wide end)
# Random direction, then place source dot at that end.
active_directed = []
for i, j in active_set:
    if random.random() < 0.5:
        active_directed.append((i, j))  # i = source (wide)
    else:
        active_directed.append((j, i))  # j = source (wide)

print(f"Active edges: {len(active_set)}")

# ---------------------------------------------------------------------------
# 5. Rotation & Projection
# ---------------------------------------------------------------------------
POLAR = math.radians(80)
AZIMUTH = math.radians(140)


def rotate(v, polar, azimuth):
    x, y, z = v
    ca, sa = math.cos(azimuth), math.sin(azimuth)
    x1 = x * ca + z * sa
    z1 = -x * sa + z * ca
    y1 = y
    cp, sp = math.cos(polar), math.sin(polar)
    y2 = y1 * cp - z1 * sp
    z2 = y1 * sp + z1 * cp
    return (x1, y2, z2)


rotated = [rotate(v, POLAR, AZIMUTH) for v in verts]
xs = [v[0] for v in rotated]
ys = [v[1] for v in rotated]
max_ext = max(abs(min(xs)), abs(max(xs)), abs(min(ys)), abs(max(ys)))
TARGET_RADIUS = 215
SCALE = TARGET_RADIUS / max_ext
CX, CY = 256, 256


def project(v):
    x, y, z = v
    return (CX + x * SCALE, CY + y * SCALE)


proj = [project(v) for v in rotated]

# ---------------------------------------------------------------------------
# 6. Tapered polygon helper
# ---------------------------------------------------------------------------
HEAD_W = 6.4  # packet wide end
TAIL_W = 0.0  # packet tip — tapers to nothing


def tapered_edge(x1, y1, x2, y2):
    """Triangle polygon from (x1,y1) wide end tapering to (x2,y2) tip."""
    dx, dy = x2 - x1, y2 - y1
    length = math.hypot(dx, dy)
    if length < 0.01:
        return None
    px, py = -dy / length, dx / length
    hw = HEAD_W / 2
    pts = [
        (x1 + px * hw, y1 + py * hw),
        (x2, y2),
        (x1 - px * hw, y1 - py * hw),
    ]
    coord_str = ' '.join(f'{x:.2f},{y:.2f}' for x, y in pts)
    return f'<polygon points="{coord_str}" fill="white"/>'


# ---------------------------------------------------------------------------
# 7. SVG generation
# ---------------------------------------------------------------------------
svg_lines = []


def L(s):
    svg_lines.append(s)


L('<?xml version="1.0" encoding="UTF-8"?>')
L('<svg xmlns="http://www.w3.org/2000/svg" width="512" height="512" viewBox="0 0 512 512">')
L('<defs>')
L('<linearGradient id="bg" gradientUnits="userSpaceOnUse" x1="256" y1="512" x2="256" y2="0">')
L('<stop offset="0" stop-color="#0882C8"/>')
L('<stop offset="1" stop-color="#26B6DB"/>')
L('</linearGradient>')
L('</defs>')

# Blue gradient circle
L(f'<circle cx="256" cy="256" r="256" fill="url(#bg)"/>')

# Passive edges (dashed)
DASH_OPACITY = 0.67
active_undirected = {(min(i, j), max(i, j)) for i, j in active_set}
for i, j in edge_list:
    if (i, j) in active_undirected:
        continue
    x1, y1 = proj[i]
    x2, y2 = proj[j]
    L(f'<line x1="{x1:.2f}" y1="{y1:.2f}" x2="{x2:.2f}" y2="{y2:.2f}" '
      f'stroke="white" stroke-width="2" stroke-dasharray="12,8" '
      f'stroke-linecap="round" opacity="{DASH_OPACITY}"/>')

# Active edges — data packet with glow layers around tapered core
COMET_R = HEAD_W / 2   # 3.2, matches core polygon width
for src, dst in active_directed:
    x1, y1 = proj[src]
    x2, y2 = proj[dst]
    dx = x2 - x1
    dy = y2 - y1
    length = math.hypot(dx, dy)
    if length < 0.01:
        continue

    # Random packet progress
    progress = random.uniform(0.35, 0.85)
    px = x1 + dx * progress
    py = y1 + dy * progress

    # Background track — same style as passive dashed edges
    L(f'<line x1="{x1:.2f}" y1="{y1:.2f}" x2="{x2:.2f}" y2="{y2:.2f}" '
      f'stroke="white" stroke-width="2" stroke-dasharray="12,8" '
      f'stroke-linecap="round" opacity="0.67"/>')

    # Outer glow (expands outward from core)
    L(f'<line x1="{x1:.2f}" y1="{y1:.2f}" x2="{px:.2f}" y2="{py:.2f}" '
      f'stroke="white" stroke-width="16" stroke-linecap="round" opacity="0.10"/>')
    # Middle glow
    L(f'<line x1="{x1:.2f}" y1="{y1:.2f}" x2="{px:.2f}" y2="{py:.2f}" '
      f'stroke="white" stroke-width="10" stroke-linecap="round" opacity="0.25"/>')

    # Core tapered polygon: wide at leading edge (comet head), narrows toward source
    poly = tapered_edge(px, py, x1, y1)
    if poly:
        L(poly)

    # Comet head at leading edge (matches core width)
    L(f'<circle cx="{px:.2f}" cy="{py:.2f}" r="{COMET_R:.1f}" fill="white"/>')


L('</svg>')

result = '\n'.join(svg_lines)
with open('assets/syncdesic-logo-only.svg', 'w', encoding='utf-8') as f:
    f.write(result)

print("SVG written to assets/syncdesic-logo-only.svg")
print(f"SVG size: {len(result)} bytes")
