# Prints Calibre's own title_sort results as JSON, for
# internal/findings/testdata/title_sort_golden.json. Run through
# scripts/title-sort-golden.sh with calibre-debug; every title is invented.
import json
import sys

from calibre.ebooks.metadata import title_sort
from calibre.utils.config_base import tweaks

articles = tweaks['per_language_title_sort_articles']
quotes = '"\'“”„‚’‘‹›《〈»«「『'
closers = {'"': '"', "'": "'", '“': '”', '”': '”', '„': '“', '‚': '‘', '’': '’', '‘': '’',
           '‹': '›', '›': '‹', '《': '》', '〈': '〉', '»': '«', '«': '»', '「': '」', '『': '』'}

cases = [
    ('Die Welt', 'deu'), ('Die Welt', None), ('El Quijote', 'spa'), ('The Book', 'deu'),
    ('Das Boot', 'deu'), ('The Book', None), ('The Book', 'xyz'), ('The Book', 'jpn'),
    ('"The Quote"', 'eng'), ("'The' Book", 'eng'), ('The "Book"', 'eng'), ('«The Book»', 'eng'),
    ('The:Book', 'eng'), ('THE BOOK', 'eng'), ('the book', 'eng'), ('De La Terre', 'fra'),
    ("L'Étranger", 'fra'), ('  The  Space ', 'eng'), ('A', 'eng'), ('The', 'eng'),
    ('Theory', 'eng'), ('Thé Book', 'eng'), ('The\tBook', 'eng'), ('The Book', 'eng'),
    ('The\u001cBook', 'eng'), ('The Book', 'eng'), ('', 'eng'), ('   ', 'eng'),
    ("'n Boek", 'afr'), ('Des Choses', 'fra'), ('Nişte Cărți', 'ron'), ('Die Welt', 'und'),
    ('Die Welt', 'de'), ('Der Prozess', 'ger'), ('Ogniem i mieczem', 'pol'), ('A Pole', 'pol'),
    ('An Apple', 'eng'), ('Le Petit Prince', 'fra'), ('Het Achterhuis', 'nld'), ("'t Hooft", 'nld'),
    ('Il Nome', 'ita'), ("Dell'Amore", 'ita'), ('Os Lusíadas', 'por'), ('Bir Gün', 'tur'),
    ('Ett År', 'swe'), ('Az Ember', 'hun'), ('La Espero', 'epo'), ('To Vivlio', 'ell'),
    ('Die Welt', 'eng'), ('The Book', 'en'), ('The Book', 'EN-gb'), ('The Book', 'eng@x'),
]
# One title per article in every language's table.
for lang, pats in sorted(articles.items()):
    for pat in sorted(pats):
        word = pat.replace('\\s+', ' ').replace('(La\\s+)?', '').replace('\\s', ' ')
        cases.append((word + 'Title', lang) if not word.endswith(' ') else (word + 'Title Here', lang))
for q in quotes:
    cases.append((q + 'The Book' + closers[q], 'eng'))
    cases.append((q + 'The Book', 'eng'))

json.dump([{'title': t, 'lang': l, 'sort': title_sort(t, lang=l)} for t, l in cases],
          sys.stdout, ensure_ascii=False, indent=1)
