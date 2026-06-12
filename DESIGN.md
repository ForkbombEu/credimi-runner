---
version: alpha
name: Puria Neubrutalist
description: Mandatory visual identity for every Puria web UI, TUI, and design-bearing artifact.
colors:
  primary: "#000000"
  secondary: "#FFFDF5"
  tertiary: "#FFD23F"
  accent-pink: "#FF6B6B"
  accent-blue: "#74B9FF"
  accent-green: "#88D498"
  accent-orange: "#FFA552"
  accent-lavender: "#B8A9FA"
typography:
  display:
    fontFamily: Syne
    fontSize: 48px
    fontWeight: 800
    lineHeight: 1
    letterSpacing: 0em
  heading:
    fontFamily: Space Grotesk
    fontSize: 28px
    fontWeight: 700
    lineHeight: 1.15
    letterSpacing: 0em
  body:
    fontFamily: Inter
    fontSize: 16px
    fontWeight: 400
    lineHeight: 1.55
    letterSpacing: 0em
  label:
    fontFamily: Space Mono
    fontSize: 12px
    fontWeight: 700
    lineHeight: 1.2
    letterSpacing: 0.06em
rounded:
  none: 0px
  tight: 2px
  max: 4px
spacing:
  xs: 4px
  sm: 8px
  md: 16px
  lg: 24px
  xl: 40px
components:
  surface:
    backgroundColor: "{colors.secondary}"
    textColor: "{colors.primary}"
    typography: "{typography.body}"
    rounded: "{rounded.none}"
    padding: "{spacing.md}"
  button-primary:
    backgroundColor: "{colors.tertiary}"
    textColor: "{colors.primary}"
    typography: "{typography.label}"
    rounded: "{rounded.none}"
    padding: "{spacing.sm}"
  input:
    backgroundColor: "{colors.secondary}"
    textColor: "{colors.primary}"
    typography: "{typography.body}"
    rounded: "{rounded.none}"
    padding: "{spacing.sm}"
  badge:
    backgroundColor: "{colors.accent-blue}"
    textColor: "{colors.primary}"
    typography: "{typography.label}"
    rounded: "{rounded.tight}"
    padding: "{spacing.xs}"
  danger:
    backgroundColor: "{colors.accent-pink}"
    textColor: "{colors.primary}"
    typography: "{typography.label}"
    rounded: "{rounded.none}"
    padding: "{spacing.sm}"
  success:
    backgroundColor: "{colors.accent-green}"
    textColor: "{colors.primary}"
    typography: "{typography.label}"
    rounded: "{rounded.none}"
    padding: "{spacing.sm}"
  warning:
    backgroundColor: "{colors.accent-orange}"
    textColor: "{colors.primary}"
    typography: "{typography.label}"
    rounded: "{rounded.none}"
    padding: "{spacing.sm}"
  tertiary-accent:
    backgroundColor: "{colors.accent-lavender}"
    textColor: "{colors.primary}"
    typography: "{typography.label}"
    rounded: "{rounded.none}"
    padding: "{spacing.sm}"
---

# Puria Neubrutalist Design

## Overview

All Puria design is neubrutalist. The interface must declare its structure instead of hiding it: blunt edges, visible borders, flat color, hard offset depth, bold type, and a small number of loud accent colors.

The design reference is Neubrutalism. Web UI must follow that reference directly. TUI work must translate the same principles into terminal constraints: boxed regions, high contrast, clear hierarchy, explicit grouping, and minimal ornamental softness.

Design must remain usable. Neubrutalism here means emphatic and structured, not messy, inaccessible, or randomly chaotic.

## Colors

The palette uses a black structural base, warm off-white surfaces, and a limited set of saturated flat accents.

- **Primary Black (#000000):** borders, text, shadows, dividers, icons, and structural marks.
- **Off-White (#FFFDF5):** default page and panel background.
- **Bold Yellow (#FFD23F):** primary actions, important highlights, and active states.
- **Coral Pink (#FF6B6B):** destructive actions, warnings, and urgent accents.
- **Sky Blue (#74B9FF):** secondary highlights, badges, and information states.
- **Soft Green (#88D498):** success states and positive confirmations.
- **Orange (#FFA552):** attention accents that are not errors.
- **Lavender (#B8A9FA):** optional tertiary accent, used sparingly.

Do not use gradients, translucent glass effects, blur, low-contrast gray-on-gray UI, or ambient shadows.

## Typography

Use bold display type for major moments, a strong geometric sans for headings, readable sans text for body copy, and monospace for labels, tokens, and technical metadata.

- **Display:** Syne 800, large, blunt, and reserved for hero-scale text or major section identity.
- **Heading:** Space Grotesk 700 for cards, panels, navigation groups, and section headings.
- **Body:** Inter 400 for readable operational text.
- **Label:** Space Mono 700 for small labels, badges, counters, metadata, shortcuts, and CLI/TUI identity.

Letter spacing stays at `0` for normal text. Use uppercase sparingly for labels only.

## Layout

Use explicit grids and obvious grouping. Panels, cards, forms, and tool regions should look assembled from discrete blocks.

Layouts may use controlled asymmetry, offsets, or stacked panels, but reading order and task flow must stay predictable. Dense operational screens should prioritize scanability over spectacle.

For TUI work, use box borders, aligned columns, clear section labels, visible focus, and high-contrast state markers.

## Elevation & Depth

Depth is hard, offset, and graphic. Use zero blur.

- **Small:** `3px 3px 0 0 #000000` for badges, chips, inline controls, and minor affordances.
- **Medium:** `5px 5px 0 0 #000000` for buttons, cards, panels, and primary interactive elements.
- **Large:** `8px 8px 0 0 #000000` for modals, hero elements, overlays, and high-priority focus.

Do not use soft shadows, ambient elevation, glow, backdrop blur, or glassmorphism.

## Shapes

Corners are square by default.

- Default radius: `0px`
- Maximum ordinary radius: `4px`
- Avoid pills unless the product domain already requires them.

Borders are structural and should usually be `2px` or `3px` solid black. Deviate only for hierarchy or terminal constraints.

## Components

Buttons, inputs, cards, modals, sidebars, tabs, menus, tables, banners, and command surfaces must use visible borders and clear state changes.

Primary buttons use yellow fill, black border, black text, and hard shadow. Hover or active states may shift the shadow offset, invert colors, or translate the element by a few pixels.

Inputs use off-white fill, black border, no soft glow, and explicit focus states. Errors must use both color and text or iconography, never color alone.

Svelte projects should prefer existing neobrutalist Svelte components when they fit the task. If they do not fit, build custom components that follow these tokens and rules.

## Do's and Don'ts

Do:

- Use thick borders, hard shadows, flat fills, and obvious grouping.
- Keep body text readable and operational UI scannable.
- Maintain WCAG AA contrast for body text and interactive controls.
- Use one neutral base, black structure, and one to three accents per screen.
- Make state, focus, and hierarchy visible.

Don't:

- Use gradients, blurred shadows, glass effects, muted SaaS neutrals, or decorative softness.
- Let every element compete at maximum saturation.
- Sacrifice reading order or task completion for visual attitude.
- Rely on color alone to communicate meaning.
- Create a non-neubrutalist UI for Puria projects.
