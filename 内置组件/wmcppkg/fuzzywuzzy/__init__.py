# -*- coding: utf-8 -*-
"""fuzzywuzzy → thefuzz 兼容 shim（本产品自有代码，Apache-2.0）。

背景：windows-mcp 0.8.8 的 desktop/service.py 里 `from fuzzywuzzy import
process`，而 fuzzywuzzy 0.18.0 是 GPLv2（SeatGeek），其官方继任 thefuzz
（MIT，API 同名兼容，RapidFuzz 加速）已随 wmcppkg 分发。为了既不改
windows-mcp 的代码、又不随包分发 GPL 包，原包已于 2026-11-05 从分发清单
移除（见仓库根 THIRD-PARTY-NOTICES.md），本目录改为这几行转发代码：
import fuzzywuzzy 实际落到 thefuzz。
"""

from thefuzz import fuzz, process  # noqa: F401

__version__ = "0.18.0+thefuzz-shim"
