import requests
from bs4 import BeautifulSoup
import logging

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

WIKIPEDIA_API_URL = "https://en.wikipedia.org/w/api.php"
HEADERS = {'User-Agent': 'UVA-Author-Enrichment-Bot/1.0'}

def fetch_notable_works(title):
    params_sections = {
        "action": "parse",
        "prop": "sections",
        "page": title,
        "format": "json"
    }
    try:
        resp = requests.get(WIKIPEDIA_API_URL, params=params_sections, headers=HEADERS, timeout=15)
        resp.raise_for_status()
        data = resp.json()
        sections = data.get("parse", {}).get("sections", [])
        
        target_indices = []
        target_keywords = ['works', 'bibliography', 'selected works', 'selected bibliography', 'notable works', 'publications']
        for s in sections:
            if s['line'].lower() in target_keywords:
                target_indices.append(s['index'])
        
        if not target_indices:
            return []

        all_works = []
        for index in target_indices[:1]:
            params_content = {
                "action": "parse",
                "prop": "text",
                "page": title,
                "section": index,
                "format": "json",
                "disablelimitreport": 1,
                "disabletoc": 1
            }
            resp = requests.get(WIKIPEDIA_API_URL, params=params_content, headers=HEADERS, timeout=15)
            resp.raise_for_status()
            content_data = resp.json()
            html = content_data.get("parse", {}).get("text", {}).get("*", "")
            
            if html:
                soup = BeautifulSoup(html, 'html.parser')
                items = soup.find_all('li')
                for item in items:
                    for ref in item.find_all(['sup', 'ref']):
                        ref.decompose()
                    text = item.get_text().strip()
                    if text and len(text) > 3:
                        all_works.append(text)
        
        return all_works[:15]
    except Exception as e:
        logger.warning(f"Failed to fetch works for {title}: {e}")
        return []

title = "A. A. Milne"
print(f"Testing works extraction for {title}...")
works = fetch_notable_works(title)
for i, w in enumerate(works):
    print(f"{i+1}. {w}")
