"""Windows-MCP 的启动垫片（由内置 MCP 连接器直接调用）。

存在的唯一原因：技能区那个便携 Python 是 embeddable 发行版，目录里的
python314._pth 会让解释器进入 isolated 模式——PYTHONPATH 一律不生效。
而 Windows-MCP 的依赖又不能装进运行时（技能/_runtime 会被分块更新整目录
替换，第三方依赖会被冲掉），所以依赖放在 内置组件/wmcppkg，由这里显式
插进 sys.path 再转交上游入口。

用法：python windows-mcp-boot.py serve
"""

import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
_PKG = os.path.join(_HERE, "wmcppkg")
if _PKG not in sys.path:
    sys.path.insert(0, _PKG)

from windows_mcp.__main__ import main

if __name__ == "__main__":
    main()
