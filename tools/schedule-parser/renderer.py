from __future__ import annotations

import json
from pathlib import Path
from typing import Iterable

from parser import Lesson

DEFAULT_COLORS = {
    "1": "#2F8CFF", "2": "#35D6B2", "3": "#A66CFF", "4": "#FFB45C",
    "5": "#4FD8FF", "6": "#FF6F91", "7": "#7C8CFF", "8": "#65D38A",
    "9": "#D58CFF", "10": "#FFD166",
}


def load_colors(path: str | None) -> dict[str, str]:
    colors = dict(DEFAULT_COLORS)
    if path:
        data = json.loads(Path(path).read_text(encoding="utf-8"))
        colors.update({str(k): str(v) for k, v in data.get("lesson_colors", {}).items()})
    return colors


def _lesson_payload(lessons: list[Lesson], colors: dict[str, str]) -> list[dict[str, object]]:
    return [
        {
            "date": lesson.date,
            "day": lesson.day,
            "lesson_number": lesson.lesson_number,
            "time": lesson.time,
            "group": lesson.group,
            "subgroup": lesson.subgroup,
            "discipline": lesson.discipline,
            "lesson_type": lesson.lesson_type,
            "room": lesson.room,
            "teacher": lesson.teacher,
            "color": colors.get(str(lesson.lesson_number), "#3b82f6"),
        }
        for lesson in lessons
    ]


def render_html(
    lessons: Iterable[Lesson],
    colors: dict[str, str],
    title: str = "Расписание",
    month: int | None = None,
    year: int | None = None,
) -> str:
    """Render a deliberately minimal page used to develop the interactive background.

    Schedule data is still embedded for compatibility with the parser pipeline, but
    no previous Liquid Glass/schedule UI is rendered here. The entire viewport is the
    interactive character grid.
    """
    lessons = list(lessons)
    payload = _lesson_payload(lessons, colors)
    data_json = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).replace("</", "<\\/")
    safe_title = title.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;").replace('"', "&quot;")

    return f'''<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#030914">
<title>{safe_title}</title>

</head>
<body>
<canvas id="gridCanvas" aria-hidden="true"></canvas>
<div id="cursorRing" aria-hidden="true"></div>

<script>
// Schedule data remains available to future UI work, but this version intentionally
// renders ONLY the background.
const LESSONS = {data_json};

const canvas = document.getElementById('gridCanvas');
const ctx = canvas.getContext('2d', {{ alpha: false }});
const cursorRing = document.getElementById('cursorRing');

// -------------------------
// Background configuration
// -------------------------
const CHARS = '01<>[]{{}}()/\\|+-=*#@$%&:;._';
const CELL_DESKTOP = 44;
const CELL_TABLET = 38;
const CELL_MOBILE = 30;
const REVEAL_RADIUS = 2.35;       // roughly two cells in every direction
const CORE_RADIUS = 0.72;
const DECAY_MS = 650;
const CHANGE_MS = 95;
const TRAIL_DECAY_MS = 1050;
const MAX_ALPHA = 0.62;

let width = 0;
let height = 0;
let dpr = 1;
let cellSize = CELL_DESKTOP;
let cols = 0;
let rows = 0;

// key -> cell state
const cells = new Map();

let pointer = {{ x: -9999, y: -9999, active: false }};
let lastPointer = {{ x: -9999, y: -9999 }};
let lastFrame = performance.now();

function randomChar() {{
  return CHARS[Math.floor(Math.random() * CHARS.length)];
}}

function key(x, y) {{ return x + ':' + y; }}

function configureGrid() {{
  if (width <= 760) cellSize = CELL_MOBILE;
  else if (width <= 1100) cellSize = CELL_TABLET;
  else cellSize = CELL_DESKTOP;

  cols = Math.ceil(width / cellSize) + 1;
  rows = Math.ceil(height / cellSize) + 1;
}}

function resize() {{
  width = window.innerWidth;
  height = window.innerHeight;
  dpr = Math.min(window.devicePixelRatio || 1, 2);

  configureGrid();

  canvas.width = Math.round(width * dpr);
  canvas.height = Math.round(height * dpr);
  canvas.style.width = width + 'px';
  canvas.style.height = height + 'px';

  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';
  ctx.font = Math.max(11, cellSize * 0.30) + 'px ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace';

  // Resize should not leave characters in the wrong cells.
  cells.clear();
}}

function revealAt(x, y, now) {{
  const centerX = Math.floor(x / cellSize);
  const centerY = Math.floor(y / cellSize);

  for (let dy = -2; dy <= 2; dy++) {{
    for (let dx = -2; dx <= 2; dx++) {{
      const distance = Math.hypot(dx, dy);
      if (distance > REVEAL_RADIUS) continue;

      const gx = centerX + dx;
      const gy = centerY + dy;
      if (gx < 0 || gy < 0 || gx > cols || gy > rows) continue;

      // Core cells are brighter; the outer cells form a soft circular contour.
      const proximity = Math.max(0, 1 - distance / REVEAL_RADIUS);
      const target = Math.pow(proximity, 0.72);
      const k = key(gx, gy);
      const existing = cells.get(k);

      cells.set(k, {{
        x: gx,
        y: gy,
        born: now,
        lastChanged: now,
        alpha: Math.max(existing ? existing.alpha : 0, target),
        target,
        char: randomChar(),
        trail: Math.max(existing ? existing.trail : 0, target),
      }});
    }}
  }}
}}

function revealAlongPath(x1, y1, x2, y2, now) {{
  const distance = Math.hypot(x2 - x1, y2 - y1);
  // Prevent fast cursor movement from skipping over cells.
  const steps = Math.max(1, Math.ceil(distance / (cellSize * 0.42)));
  for (let i = 1; i <= steps; i++) {{
    const t = i / steps;
    revealAt(x1 + (x2 - x1) * t, y1 + (y2 - y1) * t, now);
  }}
}}

function update(now) {{
  for (const [k, cell] of cells) {{
    const age = now - cell.born;
    const sinceChange = now - cell.lastChanged;

    // Symbols mutate shortly after appearing, giving the feeling of a live grid.
    if (sinceChange > CHANGE_MS) {{
      const mutationChance = Math.min(0.34, (sinceChange - CHANGE_MS) / 850);
      if (Math.random() < mutationChance * 0.08) {{
        cell.char = randomChar();
        cell.lastChanged = now;
      }}
    }}

    // Bright initial reveal -> soft trail -> disappearance.
    const fade = Math.max(0, 1 - Math.max(0, age - 40) / DECAY_MS);
    const trailFade = Math.max(0, 1 - age / TRAIL_DECAY_MS);
    cell.alpha = Math.min(cell.alpha, Math.max(fade * cell.target, trailFade * cell.target * 0.58));
    cell.trail = trailFade * cell.target;

    if (age > TRAIL_DECAY_MS || cell.alpha < 0.012) cells.delete(k);
  }}
}}

function drawGrid() {{
  const now = performance.now();
  update(now);

  // Deep, nearly-black blue base.
  ctx.fillStyle = '#030914';
  ctx.fillRect(0, 0, width, height);

  // Empty grid lines.
  ctx.lineWidth = 1;
  ctx.strokeStyle = getComputedStyle(document.documentElement).getPropertyValue('--grid-line').trim();
  ctx.beginPath();

  for (let x = 0; x <= cols; x++) {{
    const px = Math.round(x * cellSize) + 0.5;
    ctx.moveTo(px, 0);
    ctx.lineTo(px, height);
  }}
  for (let y = 0; y <= rows; y++) {{
    const py = Math.round(y * cellSize) + 0.5;
    ctx.moveTo(0, py);
    ctx.lineTo(width, py);
  }}
  ctx.stroke();

  // Revealed characters.
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';
  ctx.font = Math.max(11, cellSize * 0.30) + 'px ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace';

  for (const cell of cells.values()) {{
    const px = cell.x * cellSize + cellSize / 2;
    const py = cell.y * cellSize + cellSize / 2;
    const alpha = Math.min(MAX_ALPHA, cell.alpha * 0.78 + cell.trail * 0.16);

    // A subtle stronger edge around the cursor reaction.
    const isCore = pointer.active && Math.hypot(
      px - pointer.x,
      py - pointer.y
    ) < cellSize * CORE_RADIUS;

    if (isCore) {{
      ctx.fillStyle = 'rgba(151, 214, 255, ' + Math.min(0.9, alpha + 0.18) + ')';
      ctx.shadowColor = 'rgba(76, 166, 255, .42)';
      ctx.shadowBlur = 10;
    }} else {{
      ctx.fillStyle = 'rgba(' + getComputedStyle(document.documentElement).getPropertyValue('--character').trim() + ', ' + alpha + ')';
      ctx.shadowColor = 'rgba(65, 150, 255, ' + Math.min(0.24, alpha * 0.45) + ')';
      ctx.shadowBlur = alpha > 0.22 ? 5 : 2;
    }}

    ctx.fillText(cell.char, px, py);
  }}

  ctx.shadowBlur = 0;
  lastFrame = now;
  requestAnimationFrame(drawGrid);
}}

window.addEventListener('resize', resize, {{ passive: true }});

window.addEventListener('pointermove', (event) => {{
  const now = performance.now();
  const x = event.clientX;
  const y = event.clientY;

  if (!pointer.active) revealAt(x, y, now);
  else revealAlongPath(lastPointer.x, lastPointer.y, x, y, now);

  pointer.x = x;
  pointer.y = y;
  pointer.active = true;
  lastPointer.x = x;
  lastPointer.y = y;

  cursorRing.style.left = x + 'px';
  cursorRing.style.top = y + 'px';
  cursorRing.classList.add('active');
}}, {{ passive: true }});

window.addEventListener('pointerleave', () => {{
  pointer.active = false;
  cursorRing.classList.remove('active');
}});

// Touch devices have no persistent cursor; avoid creating a permanent animation.
window.addEventListener('touchstart', () => {{
  pointer.active = false;
  cursorRing.classList.remove('active');
}}, {{ passive: true }});

resize();
drawGrid();
</script>
</body>
</html>'''
