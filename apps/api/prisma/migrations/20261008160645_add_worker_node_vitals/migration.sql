-- AlterTable
ALTER TABLE "worker_nodes" ADD COLUMN     "last_vitals" JSONB,
ADD COLUMN     "last_vitals_at" TIMESTAMPTZ;
