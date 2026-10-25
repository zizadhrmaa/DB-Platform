-- Database target contoh "toko". Sengaja memuat berbagai objek agar
-- viewer bisa diuji: beberapa schema, PK, FK, UNIQUE, CHECK, index biasa,
-- index parsial, composite PK, view, NULL, JSONB, array, tipe numeric,
-- nama tabel dengan huruf besar/spasi, dan >200 baris untuk pagination.

CREATE SCHEMA inventory;

CREATE TABLE public.customers (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email      TEXT NOT NULL UNIQUE,
    full_name  TEXT NOT NULL,
    phone      TEXT,                                  -- boleh NULL
    tags       TEXT[] NOT NULL DEFAULT '{}',
    profile    JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE inventory.products (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    sku         TEXT NOT NULL,
    name        TEXT NOT NULL,
    price_minor BIGINT NOT NULL CHECK (price_minor >= 0),  -- rupiah, tanpa desimal
    stock       INTEGER NOT NULL DEFAULT 0 CHECK (stock >= 0),
    is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    CONSTRAINT products_sku_uq UNIQUE (sku)
);
CREATE INDEX products_name_idx ON inventory.products (lower(name));
CREATE INDEX products_active_idx ON inventory.products (id) WHERE is_active;

CREATE TABLE public.orders (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id BIGINT NOT NULL REFERENCES public.customers(id) ON DELETE RESTRICT,
    status      TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new','paid','shipped','cancelled')),
    total_minor NUMERIC(14,0) NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX orders_customer_idx ON public.orders (customer_id, created_at DESC);

CREATE TABLE public.order_items (
    order_id   BIGINT NOT NULL REFERENCES public.orders(id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL REFERENCES inventory.products(id),
    qty        INTEGER NOT NULL CHECK (qty > 0),
    PRIMARY KEY (order_id, product_id)                 -- composite PK
);

-- Nama "aneh" untuk menguji quoting identifier di viewer.
CREATE TABLE public."Audit Log" (
    id   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    "Event Name" TEXT NOT NULL,
    at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE VIEW public.order_summary AS
SELECT o.id, c.full_name, o.status, o.total_minor
FROM public.orders o JOIN public.customers c ON c.id = o.customer_id;

INSERT INTO public.customers (email, full_name, phone, tags, profile)
SELECT 'user' || g || '@contoh.id', 'Pelanggan ' || g,
       CASE WHEN g % 3 = 0 THEN NULL ELSE '08' || lpad(g::text, 10, '0') END,
       CASE WHEN g % 2 = 0 THEN ARRAY['vip'] ELSE '{}' END,
       CASE WHEN g % 5 = 0 THEN jsonb_build_object('kota', 'Bandung', 'poin', g) END
FROM generate_series(1, 250) g;

INSERT INTO inventory.products (sku, name, price_minor, stock, is_active)
SELECT 'SKU-' || lpad(g::text, 4, '0'), 'Produk ' || g, g * 1000, g % 50, g % 7 <> 0
FROM generate_series(1, 120) g;

INSERT INTO public.orders (customer_id, status, total_minor)
SELECT (g % 250) + 1, (ARRAY['new','paid','shipped','cancelled'])[(g % 4) + 1], g * 15000
FROM generate_series(1, 400) g;

INSERT INTO public.order_items (order_id, product_id, qty)
SELECT g, (g % 120) + 1, (g % 5) + 1 FROM generate_series(1, 400) g;

INSERT INTO public."Audit Log" ("Event Name") VALUES ('seed'), ('<script>alert(1)</script>');

-- User database dengan hak terbatas untuk dipakai platform (praktik baik:
-- platform tidak memakai superuser). Bisa baca semua & menjalankan DDL di public/inventory.
CREATE ROLE platform_app LOGIN PASSWORD 'platform_app_pass';
GRANT USAGE, CREATE ON SCHEMA public, inventory TO platform_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public, inventory TO platform_app;
ALTER TABLE public.customers OWNER TO platform_app;
ALTER TABLE inventory.products OWNER TO platform_app;
ALTER TABLE public.orders OWNER TO platform_app;
ALTER TABLE public.order_items OWNER TO platform_app;
ALTER TABLE public."Audit Log" OWNER TO platform_app;
ALTER VIEW public.order_summary OWNER TO platform_app;