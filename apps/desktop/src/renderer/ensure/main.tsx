// Shell page "ensure" (issue #480, docs/specs/desktop.md 4.12): "Install what's missing", the state of the node and its one button.
import { mountPage } from '../shared/mount';
import { EnsurePage } from './EnsurePage';

mountPage(<EnsurePage bridge={window.localaiDesktop} />);
