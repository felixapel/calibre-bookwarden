# Prints Calibre's own author_to_author_sort results as JSON, for
# internal/findings/testdata/author_sort_golden.json. Run through
# scripts/author-sort-golden.sh with calibre-debug; every name is invented.
import json
import sys

from calibre.ebooks.metadata import author_to_author_sort
from calibre.utils.config_base import tweaks

names = [
    'Ada Quill', 'Ada B. Quill', 'Quill', '', '   ', 'Ada  Quill', 'Ada\tQuill', 'Ada Quill',
    ' Ada Quill ', 'Quill, Ada', 'Ada Quill, Jr.', 'Ada von Quill', 'Ada le Quill', 'Ada de la Quill',
    'A.B.C. Quill', 'J.R.R. Quill', 'A. B. Quill', 'Ada Quill (Editor)', 'Ada (B) Quill', 'Ada Quill [ed.]',
    'Ada Quill {x}', 'Ada (B (C) D) Quill', 'Ada Quill)', 'Ada ( Quill', 'Ada ] Quill', '(Ada Quill)',
    'Ada Quill-Brook', "Ada O'Quill", 'Ada Quíll', 'Ádá Quill', 'Иван Перо', '山田 太郎', 'Ada Quill III',
    'Quill III', 'Jr. Quill', 'Ada Jr', 'Jr Sr', 'Mr Mrs', 'Mr. Ada', 'Mr Ada Quill', 'Mrs. Ada Quill Jr.',
    'Dr. Prof. Ada Quill Ph.D', 'Prof Ada Quill PhD', 'Ada Quill M.D.', 'Ada Quill MD', 'Ada Quill md',
    'ada quill jr', 'ADA QUILL JR.', 'Ada Quill I', 'Ada Quill Junior', 'Ada Quill Senior', 'Ada Quill Inc',
    'Ada Quill IV', 'I Ada', 'Ada Quill, PhD', 'Ada Quill', 'Ada\u001cQuill', 'Saint Ada Quill',
    'Ada of Quill', 'Ada the Quill', 'Ada Quill and Bea Brook', 'Ada & Bea',
]
for prefix in tweaks['author_name_prefixes']:
    names += [prefix + ' Ada Quill', prefix + '. Ada Quill', prefix.lower() + ' Ada Quill']
for suffix in tweaks['author_name_suffixes']:
    names += ['Ada Quill ' + suffix, 'Ada Quill ' + suffix + '.', 'Ada B. Quill ' + suffix.upper()]
for word in tweaks['author_name_copywords']:
    names += ['Quill ' + word, 'Quill ' + word.lower() + ' Brook', word.upper() + ' Quill']

json.dump({'tweaks': {k: tweaks[k] for k in ('author_sort_copy_method', 'author_use_surname_prefixes')},
           'cases': [{'name': n, 'sort': author_to_author_sort(n)} for n in names]},
          sys.stdout, ensure_ascii=False, indent=1)
