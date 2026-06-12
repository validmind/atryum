.PHONY: docs preview

docs:
	python3 website/scripts/md_to_html.py

preview:
	python3 -m http.server 8000 --directory website
