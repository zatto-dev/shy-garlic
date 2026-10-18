# Third-party notices

Shy Garlic itself is licensed under AGPL-3.0-or-later (see [LICENSE](LICENSE)).
This file covers third-party works the panel uses or that appear in this
repository.

## Icon collections

The panel shows service icons from two collections, merged and published by
the icon server <https://icons.zatto.dev> (source:
<https://github.com/zatto-dev/zatto-icons>). **Neither is bundled in the
source tree, the binary or the container image.** The server downloads the
icons your services use from the icon server (or from the mirror you set with
`--icons-url`), stores them in its icon directory and serves them from there.
The icon server **sanitizes and optimizes** the files (active content is
stripped, SVGs are minified with SVGO, PNG metadata chunks are dropped),
so they are modified copies, not byte-for-byte originals. The changes are
described on its attribution page, <https://icons.zatto.dev/attribution>, which
the panel links to as "Icon credits". The credit below is also shown in the
panel itself: in the footer and under the icon picker, in English and Polish.

### Dashboard Icons

- Work: Dashboard Icons — <https://github.com/homarr-labs/dashboard-icons>
- Copyright (c) 2024 Bjorn Lammers, Meier Lukas, Thomas Camlong and Homarr Labs
- License: Apache License 2.0 —
  <https://www.apache.org/licenses/LICENSE-2.0>
  (full text: <https://github.com/homarr-labs/dashboard-icons/blob/main/LICENSE>)
- NOTICE file: the repository has none (checked 2026-10-17), so there is no
  NOTICE text to carry along.
- Changes: sanitized and optimized by the icon server (see above).
- Used as: the first catalog of the merged index; where both collections have a
  name, this one wins.

### selfh.st/icons

- Work: selfh.st/icons — <https://github.com/selfhst/icons>
- Author: selfh.st (<https://selfh.st/icons>)
- License: Creative Commons Attribution 4.0 International (CC BY 4.0) —
  <https://creativecommons.org/licenses/by/4.0/>
  (full text: <https://github.com/selfhst/icons/blob/main/LICENSE>)
- Changes: sanitized and optimized by the icon server (see above).
- Used as: the second catalog of the merged index, for the names Dashboard
  Icons does not have.

### Trademarks

Product names and logos shown by the icons belong to their respective owners.
They identify the services on your dashboard and do not imply endorsement.
Neither license grants trademark rights.

### If you redistribute icons

The panel never redistributes the icons, but you might, for example by
building an `icons.zip` for `--icons` (zip mode) and handing it to others, or
by shipping an image with a pre-filled icon cache. In that case include, next
to the icons:

- for Dashboard Icons: a copy of the Apache License 2.0
  (the collection's `LICENSE` file);
- for selfh.st/icons: the attribution above (work, author, source, a link to
  CC BY 4.0) and a note of any changes you made to the files.

### Screenshots

The screenshots in `docs/img/` show icons from these collections as they appear
in the panel. The credits and licenses above apply to those icons.

## Fonts

The web fonts in `internal/server/assets/fonts/` are bundled in the binary;
their license is in
[internal/server/assets/fonts/LICENSE.txt](internal/server/assets/fonts/LICENSE.txt).
