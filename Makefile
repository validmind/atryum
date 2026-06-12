.PHONY: docs preview-docs

docs:
	python3 website/scripts/md_to_html.py

preview-docs:
	python3 -m http.server 8000 --directory website
