#!/usr/bin/env python3
"""Reduce KaTeX's katex.min.css @font-face rules to woff2 only, with absolute
/vendor/katex/fonts/ URLs.  Usage: rewrite-css.py <orig katex.min.css> > katex/katex.min.css"""
import re, sys
s = open(sys.argv[1]).read()
def fix(m):
    w = re.search(r'url\(fonts/([A-Za-z0-9_-]+\.woff2)\) format\("woff2"\)', m.group(0)).group(1)
    return re.sub(r'src:[^}]*', 'src:url(/vendor/katex/fonts/%s) format("woff2")' % w, m.group(0))
out = re.sub(r'@font-face\{[^}]*\}', fix, s)
assert 'woff"' not in out and 'truetype' not in out and 'url(fonts' not in out
sys.stdout.write(out)
