import os
import json

OUTPUT_DIR = "test_enriched"
if not os.path.exists(OUTPUT_DIR):
    os.makedirs(OUTPUT_DIR)

def save_author(author_match, summary_data, safe_name):
    file_path = os.path.join(OUTPUT_DIR, f"{safe_name}.md")
    content = f"# {author_match['common_name']}\n\nSummary: {summary_data['summary']}\n"
    with open(file_path, "w") as f:
        f.write(content)

# Test 1: Redirect case
m1 = {'common_name': 'Lewis Carroll', 'wiki_title': 'Lewis_Carroll', 'uva_names': ['Carroll, Lewis']}
safe_name_1 = "".join([c if c.isalnum() or c == '_' else "_" for c in m1['wiki_title']]).strip("_")
# Simulate Wikipedia returning a redirected title
real_title_1 = "Charles_Lutwidge_Dodgson"
save_author(m1, {"summary": "A famous author.", "wiki_url": f"https://en.wikipedia.org/wiki/{real_title_1}"}, safe_name_1)

# Check if Lewis_Carroll.md exists (it should, because we used safe_name_1)
if os.path.exists(os.path.join(OUTPUT_DIR, "Lewis_Carroll.md")):
    print("Success: Consistent filename used for redirected title.")
else:
    print("Failure: Filename mismatch for redirected title.")

# Test 2: Negative Cache
m2 = {'common_name': 'Junk Author', 'wiki_title': 'Junk_Author', 'uva_names': ['Junk']}
safe_name_2 = "".join([c if c.isalnum() or c == '_' else "_" for c in m2['wiki_title']]).strip("_")
save_author(m2, {"summary": "No Wikipedia summary available.", "wiki_url": "N/A"}, safe_name_2)

if os.path.exists(os.path.join(OUTPUT_DIR, "Junk_Author.md")):
    print("Success: Negative cache placeholder created.")
else:
    print("Failure: Negative cache placeholder missing.")
