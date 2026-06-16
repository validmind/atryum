.PHONY: docs docs-pdf preview-docs

docs:
	python3 website/scripts/md_to_html.py

docs-pdf:
	python3 website/scripts/md_to_pdf.py

preview-docs: docs
	python3 -m http.server 8000 --directory website
