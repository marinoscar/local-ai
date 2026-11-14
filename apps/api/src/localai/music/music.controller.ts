// The music routes of the cloud UI (E11, docs/specs/cloud-app.md section 3.9): make a song on a node, follow it, play,
// download and delete songs. Every route is `@Auth` with the E11 permissions and every input is Zod-validated.
// `localai_music:use` makes and plays music; `localai_music:delete` deletes songs (and sees everyone's). The workload
// switch is `workloads.controller.ts`.
import { Body, Controller, Delete, Get, Headers, HttpCode, Param, Post, Query, Req, Res } from '@nestjs/common';
import { ApiConsumes, ApiOperation, ApiTags } from '@nestjs/swagger';
import { AuthCredential, Auth, CurrentUser, type AuthCredentialInfo, type RequestUser } from '@marinoscar/platform-api/identity';
import type { FastifyReply } from 'fastify';

import { parseWith } from '../commands/command-api.schemas';
import { CLIENT_HEADER, sourceOf } from '../commands/commands.controller';
import { LOCALAI_PERMISSIONS } from '../localai.permissions';
import { musicInputRejected } from './music-errors';
import { MusicRequestFieldsSchema } from './music-input';
import { readMusicForm } from './music-multipart';
import { MusicService } from './music.service';
import {
  AudioQuerySchema,
  DeleteSongBodySchema,
  ListRequestsQuerySchema,
  ListSongsQuerySchema,
  type MusicLibrarySong,
  type MusicNodeView,
  type MusicRequestView,
  type Page,
} from './music.views';

const USE = LOCALAI_PERMISSIONS.MUSIC_USE.id;
const DELETE = LOCALAI_PERMISSIONS.MUSIC_DELETE.id;
const WORKLOADS = LOCALAI_PERMISSIONS.WORKLOADS_WRITE.id;

const requesterOf = (user: RequestUser) => ({ userId: user.id, permissions: user.permissions });

@ApiTags('localai-music')
@Controller('localai/music')
export class MusicController {
  constructor(private readonly music: MusicService) {}

  @Get('nodes')
  @Auth({ anyPermissions: [USE, WORKLOADS, LOCALAI_PERMISSIONS.LAB_READ.id] })
  @ApiOperation({ summary: 'Every enrolled node with its music readiness (ready, can_load, too_little_vram, offline, no_music) and the workloads offered' })
  nodes(): Promise<MusicNodeView[]> {
    return this.music.nodes();
  }

  @Post('requests')
  @Auth({ permissions: [USE] })
  @ApiConsumes('multipart/form-data')
  @ApiOperation({
    summary:
      'Make music on a node: multipart fields nodeId, lyrics (<= 8 KiB), tags (<= 512 B), duration_s (10-240), language, seed, variations (1-4), sample_mode, strength, and an optional file `sample` (wav/mp3/flac/ogg, <= 20 MiB). 201 with the request. 400/413/415 LAI-SVC-154, 429 LAI-SVC-153, 404 unknown node, 409 LAI-CLD-035, 422 LAI-CLD-034, 503 storage not set up',
  })
  async create(
    @Req() request: unknown,
    @CurrentUser() user: RequestUser,
    @AuthCredential() credential: AuthCredentialInfo | null,
    @Headers(CLIENT_HEADER) client: string | undefined,
    @Res({ passthrough: true }) reply: FastifyReply,
  ): Promise<MusicRequestView> {
    const form = await readMusicForm(request);
    const parsed = MusicRequestFieldsSchema.safeParse(form.fields);
    if (!parsed.success) throw musicInputRejected(parsed.error);
    if (form.sample === null && (parsed.data.sample_mode !== undefined || parsed.data.strength !== undefined)) throw musicInputRejected(null, 'sample_settings_without_sample');
    const view = await this.music.create(parsed.data, form.sample, requesterOf(user), sourceOf(credential, client));
    void reply.status(201);
    return view;
  }

  @Get('requests')
  @Auth({ permissions: [USE] })
  @ApiOperation({ summary: "Music requests, newest first: the caller's own (a holder of localai_music:delete sees all, or only theirs with ?mine=true), ?active=true|false, ?page=, ?pageSize= (<= 50)" })
  listRequests(@Query() query: unknown, @CurrentUser() user: RequestUser): Promise<Page<MusicRequestView>> {
    return this.music.listRequests(parseWith(ListRequestsQuerySchema, query), requesterOf(user));
  }

  @Get('requests/:id')
  @Auth({ permissions: [USE] })
  @ApiOperation({ summary: 'One music request with its songs, and the lyrics for their author (the page polls it while it is queued or generating)' })
  getRequest(@Param('id') id: string, @CurrentUser() user: RequestUser): Promise<MusicRequestView> {
    return this.music.getRequest(id, requesterOf(user));
  }

  @Post('requests/:id/cancel')
  @HttpCode(200)
  @Auth({ permissions: [USE] })
  @ApiOperation({ summary: 'Cancel a request no node has picked up yet (409 once a node is making it)' })
  cancel(@Param('id') id: string, @CurrentUser() user: RequestUser): Promise<MusicRequestView> {
    return this.music.cancelRequest(id, requesterOf(user));
  }

  @Get('songs')
  @Auth({ permissions: [USE] })
  @ApiOperation({ summary: 'The cloud copies of the songs the caller may see, across every node, newest first: ?nodeId=, ?mine=, ?page=, ?pageSize= (<= 50)' })
  listSongs(@Query() query: unknown, @CurrentUser() user: RequestUser): Promise<Page<MusicLibrarySong>> {
    return this.music.listSongs(parseWith(ListSongsQuerySchema, query), requesterOf(user));
  }

  @Get('songs/:id/audio')
  @Auth({ permissions: [USE] })
  @ApiOperation({
    summary:
      'A song\'s audio: 302 to a 5-minute signed URL (?download=1 for an attachment). ?as=json answers { url, expiresAt } instead, for a player that cannot send the Authorization header',
  })
  async audio(@Param('id') id: string, @Query() query: unknown, @CurrentUser() user: RequestUser, @Res({ passthrough: true }) reply: FastifyReply): Promise<{ url: string; expiresAt: string } | undefined> {
    const parsed = parseWith(AudioQuerySchema, query);
    const signed = await this.music.audioUrl(id, parsed.download, requesterOf(user));
    if (parsed.as === 'json') return signed;
    void reply.header('Cache-Control', 'no-store').redirect(signed.url, 302);
    return undefined;
  }

  @Delete('songs/:id')
  @HttpCode(200)
  @Auth({ permissions: [DELETE] })
  @ApiOperation({ summary: 'Delete a song from the library and from storage. Body { "confirm": "<song id>" } (the typed-confirmation rule)' })
  deleteSong(@Param('id') id: string, @Body() body: unknown, @CurrentUser() user: RequestUser): Promise<{ id: string; deleted: true }> {
    const parsed = parseWith(DeleteSongBodySchema, body);
    return this.music.deleteSong(id, parsed.confirm, requesterOf(user));
  }
}
