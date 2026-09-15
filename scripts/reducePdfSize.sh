#!/usr/bin/env bash
# reducePdfSize.sh by CGIL, 2019-10-31
# Manual Ghostscript experiment retained as the reference for worker behavior.
# http://milan.kupcevic.net/ghostscript-ps-pdf/

set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "Usage: $0 <pdf-file>" >&2
    exit 2
fi

pdf_file="$1"
if [[ ! -f "$pdf_file" ]]; then
    echo "PDF file not found: $pdf_file" >&2
    exit 1
fi

echo "#### pdfinfo ${pdf_file} ####"
pdfinfo "$pdf_file"
echo "#### pdfimages -list ${pdf_file} ####"
pdfimages -list "$pdf_file"
echo "#### file size of ${pdf_file} ####"
du -h -- "$pdf_file"

echo "#### Using Ghostscript /ebook (approximately 150 DPI) ####"
gs -dNOPAUSE -dBATCH -sDEVICE=pdfwrite -dCompatibilityLevel=1.4 \
    -dPDFSETTINGS=/ebook -sOutputFile=output_150DPI.pdf "$pdf_file"
echo "#### pdfimages -list output_150DPI.pdf ####"
pdfimages -list output_150DPI.pdf
echo "#### file size of output_150DPI.pdf ####"
du -h -- output_150DPI.pdf

echo "#### Using Ghostscript /screen (approximately 72 DPI) ####"
gs -dNOPAUSE -dBATCH -sDEVICE=pdfwrite -dCompatibilityLevel=1.4 \
    -dPDFSETTINGS=/screen -sOutputFile=output_72DPI.pdf "$pdf_file"
echo "#### pdfimages -list output_72DPI.pdf ####"
pdfimages -list output_72DPI.pdf
echo "#### file size of output_72DPI.pdf ####"
du -h -- output_72DPI.pdf
