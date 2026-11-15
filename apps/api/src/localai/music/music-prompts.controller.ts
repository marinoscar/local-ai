// The prompt history and the writing helpers of the music page (#568, docs/specs/cloud-app.md section 3.9). Every
// route is `@Auth` with `localai_music:use` and every body is Zod-validated (strict). A prompt is its owner's: the
// routes answer 404 for anyone else's, except DELETE, which a holder of `localai_music:delete` may also use. Nothing
// here logs a title, a description or lyrics.
import { Body, Controller, Delete, Get, Headers, HttpCode, Param, Patch, Post, Query, Res } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { AuthCredential, Auth, CurrentUser, type AuthCredentialInfo, type RequestUser } from '@marinoscar/platform-api/identity';
import type { FastifyReply } from 'fastify';

import { parseWith } from '../commands/command-api.schemas';
import { CLIENT_HEADER, sourceOf } from '../commands/commands.controller';
import { LOCALAI_PERMISSIONS } from '../localai.permissions';
import type { MusicAiAvailability } from './music-ai';
import { MusicAssistService, type TitleSuggestion } from './music-assist.service';
import { MusicPromptsService } from './music-prompts.service';
import { MusicService } from './music.service';
import { requesterOf } from './music.controller';
import {
  CreatePromptBodySchema,
  DeletePromptBodySchema,
  ListPromptsQuerySchema,
  LyricsSuggestionBodySchema,
  MakePromptBodySchema,
  TitleSuggestionBodySchema,
  UpdatePromptBodySchema,
  type MusicPromptSummary,
  type MusicPromptView,
  type MusicRequestView,
  type Page,
} from './music.views';

const USE = LOCALAI_PERMISSIONS.MUSIC_USE.id;

/** The AI caller of a route: the user and the active organization (its AI policy, caps and own key). */
const callerOf = (user: RequestUser) => ({ userId: user.id, orgId: user.activeOrgId ?? null });

@ApiTags('localai-music')
@Controller('localai/music')
export class MusicPromptsController {
  constructor(
    private readonly prompts: MusicPromptsService,
    private readonly music: MusicService,
    private readonly assist: MusicAssistService,
  ) {}

  @Get('prompts')
  @Auth({ permissions: [USE] })
  @ApiOperation({ summary: "The caller's own saved prompts, newest used first (title, description, lyrics mode, last used, ready songs): ?page=, ?pageSize= (<= 50). No lyrics" })
  list(@Query() query: unknown, @CurrentUser() user: RequestUser): Promise<Page<MusicPromptSummary>> {
    return this.prompts.list(parseWith(ListPromptsQuerySchema, query), user.id);
  }

  @Post('prompts')
  @Auth({ permissions: [USE] })
  @ApiOperation({
    summary:
      'Save a new prompt: { title? (<= 80; empty: generated), description (required, <= 500 characters), lyricsMode, lyrics, durationS, language, seed, variations, sampleMode, strength, uiLanguage, copySampleFrom? (another of your prompts, for "Save as new") }. 201',
  })
  async create(@Body() body: unknown, @CurrentUser() user: RequestUser, @Res({ passthrough: true }) reply: FastifyReply): Promise<MusicPromptView> {
    const view = await this.prompts.createFromBody(parseWith(CreatePromptBodySchema, body), user.id);
    void reply.status(201);
    return view;
  }

  @Get('prompts/:id')
  @Auth({ permissions: [USE] })
  @ApiOperation({ summary: 'One of your saved prompts, with its lyrics (404 for anyone else\'s)' })
  get(@Param('id') id: string, @CurrentUser() user: RequestUser): Promise<MusicPromptView> {
    return this.prompts.get(id, user.id);
  }

  @Patch('prompts/:id')
  @Auth({ permissions: [USE] })
  @ApiOperation({ summary: 'Change one of your saved prompts (any field; title "" generates one; removeSample: true drops the sample). Past songs keep their own title and settings' })
  update(@Param('id') id: string, @Body() body: unknown, @CurrentUser() user: RequestUser): Promise<MusicPromptView> {
    return this.prompts.update(id, parseWith(UpdatePromptBodySchema, body), user.id);
  }

  @Delete('prompts/:id')
  @HttpCode(200)
  @Auth({ permissions: [USE] })
  @ApiOperation({ summary: 'Delete a saved prompt; its songs are kept. Body { "confirm": "<prompt id>" }. The owner, or a holder of localai_music:delete' })
  delete(@Param('id') id: string, @Body() body: unknown, @CurrentUser() user: RequestUser): Promise<{ id: string; deleted: true }> {
    return this.prompts.delete(id, parseWith(DeletePromptBodySchema, body).confirm, { userId: user.id, permissions: user.permissions });
  }

  @Post('prompts/:id/make')
  @Auth({ permissions: [USE] })
  @ApiOperation({ summary: 'Make a song from one of your saved prompts (and its stored sample): { nodeId, overrides?: { variations, seed, durationS, language } }. 201 with the request; the prompt is not changed' })
  async make(
    @Param('id') id: string,
    @Body() body: unknown,
    @CurrentUser() user: RequestUser,
    @AuthCredential() credential: AuthCredentialInfo | null,
    @Headers(CLIENT_HEADER) client: string | undefined,
    @Res({ passthrough: true }) reply: FastifyReply,
  ): Promise<MusicRequestView> {
    const view = await this.music.makeFromPrompt(id, parseWith(MakePromptBodySchema, body), requesterOf(user), sourceOf(credential, client));
    void reply.status(201);
    return view;
  }

  @Get('assist')
  @Auth({ permissions: [USE] })
  @ApiOperation({
    summary:
      'Whether music AI (Write lyrics now, an AI title, Automatic lyrics) works for you now: { ai: { enabled, reason: null | ai_off | music_ai_off | no_model, fix } }. Enabled only when AI is on (Settings, AI), Use AI for music is on, and a model resolves for you',
  })
  status(@CurrentUser() user: RequestUser): Promise<{ ai: MusicAiAvailability }> {
    return this.assist.status(callerOf(user));
  }

  @Post('title-suggestion')
  @HttpCode(200)
  @Auth({ permissions: [USE] })
  @ApiOperation({ summary: 'A title for the song: { lyrics, description, lyricsMode?, language, uiLanguage } -> { title, source: ai | generated }. Uses the cloud AI when it is on and a model resolves; otherwise, or on any AI error, the generated title' })
  title(@Body() body: unknown, @CurrentUser() user: RequestUser): Promise<TitleSuggestion> {
    const parsed = parseWith(TitleSuggestionBodySchema, body);
    return this.assist.suggestTitle(callerOf(user), { lyrics: parsed.lyricsMode === 'instrumental' ? '' : parsed.lyrics, description: parsed.description, uiLanguage: parsed.uiLanguage, language: parsed.language });
  }

  @Post('lyrics-suggestion')
  @HttpCode(200)
  @Auth({ permissions: [USE] })
  @ApiOperation({ summary: 'Write lyrics now: lyrics written by the cloud AI: { description, language, title? } -> { lyrics, source: ai }. 409 (details.reason ai_off | ai_music_off | ai_no_model, details.fix) when music AI is not available, 502 when the AI fails' })
  lyrics(@Body() body: unknown, @CurrentUser() user: RequestUser): Promise<{ lyrics: string; source: 'ai' }> {
    return this.assist.suggestLyrics(callerOf(user), parseWith(LyricsSuggestionBodySchema, body));
  }
}
