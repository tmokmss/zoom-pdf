#!/usr/bin/env python3
# Generate testdata/sample.pdf: a small, born-digital, multi-page PDF
# containing only generic Lorem-ipsum-style text. Used by integration
# and CLI smoke tests. Re-run after editing to refresh sample.pdf.
#
#   pip install reportlab
#   python3 testdata/gen_sample.py
import os
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import getSampleStyleSheet, ParagraphStyle
from reportlab.lib.units import mm
from reportlab.platypus import SimpleDocTemplate, Paragraph, Spacer, PageBreak

OUT = os.path.join(os.path.dirname(__file__), "sample.pdf")

LOREM = (
    "Lorem ipsum dolor sit amet, consectetur adipiscing elit. "
    "Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. "
    "Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris "
    "nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in "
    "reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla "
    "pariatur. Excepteur sint occaecat cupidatat non proident, sunt in "
    "culpa qui officia deserunt mollit anim id est laborum."
)

PAGES = [
    ("Chapter One: Introduction",
     "This document is a generated sample used by the zoom-pdf test suite. "
     "It contains no real or sensitive content; every paragraph is "
     "boilerplate Lorem ipsum placeholder text."),
    ("Chapter Two: Middle Section",
     "The middle of every page deliberately contains body text so that "
     "subregion extraction (for example, the center 50% of the page) "
     "still yields a non-empty set of text rectangles."),
    ("Chapter Three: Closing Notes",
     "This is the final page. The integration tests assert that a "
     "full-page zoom returns a non-empty text layer and that a centered "
     "subregion returns strictly fewer rectangles than the full page."),
]


def build():
    doc = SimpleDocTemplate(
        OUT,
        pagesize=A4,
        leftMargin=20 * mm,
        rightMargin=20 * mm,
        topMargin=20 * mm,
        bottomMargin=20 * mm,
        title="zoom-pdf sample",
        author="zoom-pdf test suite",
    )
    styles = getSampleStyleSheet()
    h1 = styles["Heading1"]
    body = ParagraphStyle("body", parent=styles["BodyText"], fontSize=11, leading=15)

    story = []
    for i, (title, intro) in enumerate(PAGES):
        story.append(Paragraph(title, h1))
        story.append(Spacer(1, 4 * mm))
        story.append(Paragraph(intro, body))
        story.append(Spacer(1, 4 * mm))
        # Repeat lorem to fill the page top-to-bottom.
        for _ in range(6):
            story.append(Paragraph(LOREM, body))
            story.append(Spacer(1, 3 * mm))
        if i < len(PAGES) - 1:
            story.append(PageBreak())

    doc.build(story)
    size = os.path.getsize(OUT)
    print(f"wrote {OUT} ({size} bytes)")


if __name__ == "__main__":
    build()
