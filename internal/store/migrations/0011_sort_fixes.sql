-- Title and author sort proposals (ADR-028) can be approved like the other
-- fields.
ALTER TABLE proposals DROP CONSTRAINT proposals_field_check;
ALTER TABLE proposals ADD CONSTRAINT proposals_field_check
    CHECK (field IN ('title', 'authors', 'languages', 'isbn', 'publisher', 'pubdate', 'title_sort', 'author_sort'));
